// Package hdr builds Dolby Vision + HDR10/HDR10+ hybrids and converts HDR10+
// into DV, driving dovi_tool and hdr10plus_tool. The active-area (crop) handling
// is automatic: we work it out from the layer geometry and the DV RPU instead of
// asking the user for pixel offsets.
package hdr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/tools"
	"github.com/720pixel/RedSync/internal/ui"
)

// HybridJob makes a DV+HDR hybrid: DV metadata (RPU) from dvSrc injected onto the
// HDR10/HDR10+ video from hdrSrc.
type HybridJob struct {
	HDRSrc  media.File // carries the HDR10 / HDR10+ base layer we keep
	DVSrc   media.File // carries the Dolby Vision RPU we want
	WorkDir string
	OutHEVC string
}

// BuildHybrid runs the whole extract -> crop-fix -> inject pipeline and returns
// the path to the finished DV+HDR HEVC elementary stream.
func BuildHybrid(ctx context.Context, j HybridJob) (string, error) {
	if len(j.HDRSrc.Video) == 0 || len(j.DVSrc.Video) == 0 {
		return "", fmt.Errorf("both sources need a video track")
	}
	hdrV, dvV := j.HDRSrc.Video[0], j.DVSrc.Video[0]
	if err := os.MkdirAll(j.WorkDir, 0o755); err != nil {
		return "", err
	}

	// multiplier > 1 means the HDR layer is bigger than the DV layer, so the
	// crop offsets baked into the RPU have to be scaled up to match.
	multiplier := 1.0
	if dvV.Height > 0 {
		multiplier = float64(hdrV.Height) / float64(dvV.Height)
	}

	hdrHEVC := filepath.Join(j.WorkDir, "hdr.hevc")
	rpu := filepath.Join(j.WorkDir, "rpu.bin")

	geo := ComputeGeometry(hdrV.Width, hdrV.Height, dvV.Width, dvV.Height)
	ui.Field("auto crop", fmt.Sprintf("top %d  bottom %d  left %d  right %d  (scale %.3f)", geo.Top, geo.Bottom, geo.Left, geo.Right, geo.Scale))

	// Two independent chains run at once: the DV side (pull the RPU, bake in the
	// crop) and the HDR side (extract the base layer). They only meet at the
	// inject. Internal step output is silenced so the single spinner stays clean
	// while both run.
	sp := ui.StartSpinner("building hybrid (extract + rpu + inject, parallel)")
	var wg sync.WaitGroup
	var dvErr, hdrErr error
	finalRPU := rpu

	wg.Add(2)
	go func() {
		defer wg.Done()
		// The DV elementary stream (tens of GB) is only needed to pull the RPU, so
		// we stream it straight from ffmpeg into dovi_tool and never stage it on
		// disk. -c crops when the DV layer is the taller one (multiplier < 1).
		crop := multiplier < 1
		if dvErr = pipeExtractRPU(ctx, j.DVSrc.Path, dvV.Index, rpu, crop, "3"); dvErr != nil {
			// older/edge streams choke on mode 3; retry without it
			if dvErr = pipeExtractRPU(ctx, j.DVSrc.Path, dvV.Index, rpu, crop, ""); dvErr != nil {
				return
			}
		}
		// Active-area handling depends on how the two layers line up:
		//   multiplier > 1  DV picture is smaller and sits inside the base, so we
		//                   add the letterbox/pillarbox bars the base needs.
		//   multiplier < 1  DV layer is the taller one (its own bars the base has
		//                   already cropped away); the -c above zeroed the area to
		//                   "no bars", which is exactly right - re-fitting here would
		//                   invent a bogus pillarbox, so we leave it zeroed.
		//   identity        same size, no bars: the RPU already fits, nothing to do.
		// The export + editor pass is also the slowest part of the DV chain, so
		// skipping it when it isn't needed is a real speed win.
		if multiplier >= 1 && !geo.identity() {
			if fixed, err := applyActiveArea(ctx, rpu, j.WorkDir, geo); err == nil && fixed != "" {
				finalRPU = fixed
			}
		}
	}()
	go func() {
		defer wg.Done()
		hdrErr = extractHEVC(ctx, j.HDRSrc.Path, hdrV.Index, hdrHEVC)
	}()
	wg.Wait()

	if dvErr != nil {
		sp.Fail("hybrid build failed")
		return "", dvErr
	}
	if hdrErr != nil {
		sp.Fail("hybrid build failed")
		return "", hdrErr
	}

	out := j.OutHEVC
	if out == "" {
		out = filepath.Join(j.WorkDir, "dv_hdr.hevc")
	}
	if err := run(ctx, tools.DoviTool, "inject-rpu", "-i", hdrHEVC, "--rpu-in", finalRPU, "-o", out); err != nil {
		sp.Fail("RPU inject failed")
		return "", err
	}
	os.Remove(hdrHEVC) // injected; the bare HDR layer isn't needed anymore
	sp.Stop("hybrid HEVC ready")
	return out, nil
}

// FromHDR10Plus synthesises a DV RPU from HDR10+ dynamic metadata and injects it,
// giving a DV stream out of an HDR10+ source.
func FromHDR10Plus(ctx context.Context, src media.File, workDir, outHEVC string) (string, error) {
	if len(src.Video) == 0 {
		return "", fmt.Errorf("source needs a video track")
	}
	v := src.Video[0]
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}
	hevc := filepath.Join(workDir, "src.hevc")
	plusJSON := filepath.Join(workDir, "hdr10plus.json")
	rpu := filepath.Join(workDir, "rpu.bin")

	ui.Step("extracting HEVC")
	if err := extractHEVC(ctx, src.Path, v.Index, hevc); err != nil {
		return "", err
	}
	ui.Step("extracting HDR10+ metadata")
	if err := run(ctx, tools.Hdr10Plus, "extract", "-i", hevc, "-o", plusJSON); err != nil {
		return "", err
	}
	ui.Step("generating DV RPU from HDR10+")
	if err := run(ctx, tools.DoviTool, "generate", "--hdr10plus-json", plusJSON, "-o", rpu); err != nil {
		return "", err
	}
	out := outHEVC
	if out == "" {
		out = filepath.Join(workDir, "dv.hevc")
	}
	ui.Step("injecting RPU")
	if err := run(ctx, tools.DoviTool, "inject-rpu", "-i", hevc, "--rpu-in", rpu, "-o", out); err != nil {
		return "", err
	}
	os.Remove(hevc) // the extracted source stream is done with
	ui.OK("DV stream ready")
	return out, nil
}

// AddHDR10Plus keeps the Dolby Vision video from dvSrc and grafts the HDR10+
// dynamic metadata carried by plusSrc onto it, producing a DV HDR10+ stream. The
// base layer and DV RPU are dvSrc's own - only the HDR10+ SEI is borrowed - so
// the result is the DV encode you already have, upgraded with HDR10+.
//
// The two sources must line up frame-for-frame (they normally do when they're
// the DV and HDR renditions of the same release); hdr10plus_tool refuses to
// inject when the counts differ, and we surface that clearly.
func AddHDR10Plus(ctx context.Context, dvSrc, plusSrc media.File, workDir, outHEVC string) (string, error) {
	if len(dvSrc.Video) == 0 {
		return "", fmt.Errorf("%s has no video track", filepath.Base(dvSrc.Path))
	}
	if len(plusSrc.Video) == 0 {
		return "", fmt.Errorf("%s has no video track", filepath.Base(plusSrc.Path))
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}
	dvV, plusV := dvSrc.Video[0], plusSrc.Video[0]
	// Profile 5 has no HDR10 base layer, so keeping its pixels can't yield a valid
	// HDR10+ file (see dvHasHDR10Base in the cli package for the same rule).
	if !dvV.HDR.HDR10 && dvV.HDR.DVProfile != 8 && dvV.HDR.DVProfile != 7 {
		return "", fmt.Errorf("%s is Dolby Vision profile %d with no HDR10 base layer; keeping its video can't make a valid HDR10+ hybrid - build with --hdr <HDR10+ file> --dv <this file> instead",
			filepath.Base(dvSrc.Path), dvV.HDR.DVProfile)
	}
	if !plusV.HDR.HDR10Plus {
		ui.Warn(filepath.Base(plusSrc.Path) + " isn't flagged HDR10+; extracting whatever dynamic metadata is present")
	}

	dvHEVC := filepath.Join(workDir, "dv.hevc")
	plusJSON := filepath.Join(workDir, "hdr10plus.json")

	// Two independent chains: keep the DV file's whole elementary stream (it's the
	// injection target, so it has to land on disk), while the HDR10+ side is only
	// mined for its metadata json and streams straight through without staging the
	// tens-of-GB HEVC.
	sp := ui.StartSpinner("building DV HDR10+ (extract dv + hdr10+ metadata, parallel)")
	var wg sync.WaitGroup
	var dvErr, plusErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		dvErr = extractHEVC(ctx, dvSrc.Path, dvV.Index, dvHEVC)
	}()
	go func() {
		defer wg.Done()
		plusErr = pipeExtractHDR10Plus(ctx, plusSrc.Path, plusV.Index, plusJSON)
	}()
	wg.Wait()
	if dvErr != nil {
		sp.Fail("extract failed")
		return "", dvErr
	}
	if plusErr != nil {
		sp.Fail("HDR10+ extract failed")
		return "", fmt.Errorf("extract HDR10+ from %s: %w", filepath.Base(plusSrc.Path), plusErr)
	}

	out := outHEVC
	if out == "" {
		out = filepath.Join(workDir, "dv_hdr10plus.hevc")
	}
	if err := run(ctx, tools.Hdr10Plus, "inject", "-i", dvHEVC, "-j", plusJSON, "-o", out); err != nil {
		sp.Fail("HDR10+ inject failed")
		return "", fmt.Errorf("inject HDR10+ (the DV and HDR10+ sources must have the same frame count): %w", err)
	}
	os.Remove(dvHEVC) // injected; the DV-only stream isn't needed anymore
	sp.Stop("DV HDR10+ HEVC ready")
	return out, nil
}

// --- streaming extraction (no multi-GB temp files) ---

// ffmpegHEVCStdout builds the ffmpeg command that writes one track's HEVC as an
// Annex B elementary stream to stdout, ready to pipe into a metadata tool.
func ffmpegHEVCStdout(ctx context.Context, path string, idx int) (*exec.Cmd, error) {
	return tools.Cmd(tools.FFmpeg,
		"-y", "-nostdin", "-loglevel", "error",
		"-i", path,
		"-map", fmt.Sprintf("0:%d", idx),
		"-c", "copy",
		"-bsf:v", "hevc_mp4toannexb",
		"-f", "hevc",
		"-",
	)
}

// pipeExtractRPU streams a track's HEVC from ffmpeg into dovi_tool extract-rpu,
// so the DV layer never lands on disk. crop adds dovi_tool's -c (used when the DV
// layer is the taller one); mode is the -m value ("" to omit).
func pipeExtractRPU(ctx context.Context, path string, idx int, rpuOut string, crop bool, mode string) error {
	ff, err := ffmpegHEVCStdout(ctx, path, idx)
	if err != nil {
		return err
	}
	var args []string
	if mode != "" {
		args = append(args, "-m", mode)
	}
	if crop {
		args = append(args, "-c")
	}
	args = append(args, "extract-rpu", "-i", "-", "-o", rpuOut)
	dv, err := tools.Cmd(tools.DoviTool, args...)
	if err != nil {
		return err
	}
	return pipe(ff, dv)
}

// pipeExtractHDR10Plus streams a track's HEVC from ffmpeg into hdr10plus_tool
// extract, mining just the metadata json without staging the HEVC.
func pipeExtractHDR10Plus(ctx context.Context, path string, idx int, jsonOut string) error {
	ff, err := ffmpegHEVCStdout(ctx, path, idx)
	if err != nil {
		return err
	}
	h, err := tools.Cmd(tools.Hdr10Plus, "extract", "-i", "-", "-o", jsonOut)
	if err != nil {
		return err
	}
	return pipe(ff, h)
}

// pipe wires producer's stdout into consumer's stdin and runs both, returning a
// clear error for whichever side fails. The consumer's error wins, since when it
// dies early the producer just sees a broken pipe.
func pipe(producer, consumer *exec.Cmd) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	producer.Stdout = w
	consumer.Stdin = r
	var pErr, cErr bytes.Buffer
	producer.Stderr = &pErr
	consumer.Stderr = &cErr

	if err := consumer.Start(); err != nil {
		w.Close()
		r.Close()
		return err
	}
	if err := producer.Start(); err != nil {
		w.Close()
		r.Close()
		_ = consumer.Wait()
		return err
	}
	// The children hold their own dups of the pipe ends; drop ours so the consumer
	// sees EOF once the producer exits.
	w.Close()
	r.Close()

	perr := producer.Wait()
	cerr := consumer.Wait()
	if cerr != nil {
		return fmt.Errorf("%s: %w\n%s", filepath.Base(consumer.Path), cerr, cErr.String())
	}
	if perr != nil {
		return fmt.Errorf("ffmpeg: %w\n%s", perr, pErr.String())
	}
	return nil
}

// --- active-area (auto crop) ---

// rpuFrame is the slice of the exported RPU json we read the crop from.
type rpuFrame struct {
	DM struct {
		CMV29 struct {
			Blocks []map[string]json.RawMessage `json:"ext_metadata_blocks"`
		} `json:"cmv29_metadata"`
	} `json:"vdr_dm_data"`
}

type level5 struct {
	Left   int `json:"active_area_left_offset"`
	Right  int `json:"active_area_right_offset"`
	Top    int `json:"active_area_top_offset"`
	Bottom int `json:"active_area_bottom_offset"`
}

// applyActiveArea writes the final active-area crop into the RPU. The crop for
// each frame is the geometric letterbox (placing the DV picture inside the HDR
// frame) plus that frame's own L5 from the DV RPU, scaled into HDR pixels. So a
// constant-bar movie gets one preset, while a variable-aspect (IMAX) title keeps
// its per-frame changes. If everything works out to zero we leave the RPU alone.
func applyActiveArea(ctx context.Context, rpu, workDir string, geo Geometry) (string, error) {
	base := geo.Base()

	// try to read the DV RPU's own per-frame L5 so variable aspect survives.
	var frames []rpuFrame
	exportJSON := filepath.Join(workDir, "rpu_export.json")
	if err := run(ctx, tools.DoviTool, "export", "-i", rpu, "-o", exportJSON); err == nil {
		if data, err := os.ReadFile(exportJSON); err == nil {
			_ = json.Unmarshal(data, &frames)
		}
		os.Remove(exportJSON) // this json runs to hundreds of MB
	}

	var presets []areaPreset
	edits := map[string]int{}

	if len(frames) == 0 {
		// no per-frame data: one constant preset from the geometry.
		if geo.zero() {
			return "", nil
		}
		presets = []areaPreset{base}
		edits["all"] = 0
	} else {
		var cur areaPreset
		start, have := 0, false
		flush := func(end int) {
			if !have {
				return
			}
			id := indexOf(presets, cur)
			if id < 0 {
				id = len(presets)
				presets = append(presets, cur)
			}
			edits[fmt.Sprintf("%d-%d", start, end)] = id
		}
		for i, f := range frames {
			p := base
			if l5, ok := readLevel5(f); ok {
				p = areaPreset{
					L: base.L + scale(l5.Left, geo.Scale),
					R: base.R + scale(l5.Right, geo.Scale),
					T: base.T + scale(l5.Top, geo.Scale),
					B: base.B + scale(l5.Bottom, geo.Scale),
				}
			}
			if !have {
				cur, start, have = p, i, true
			} else if p != cur {
				flush(i - 1)
				cur, start = p, i
			}
		}
		flush(len(frames) - 1)
		if allZero(presets) {
			return "", nil
		}
	}

	editJSON := filepath.Join(workDir, "rpu_edit.json")
	if err := writeEditJSON(editJSON, presets, edits); err != nil {
		return "", err
	}
	fixed := filepath.Join(workDir, "rpu_fixed.bin")
	if err := run(ctx, tools.DoviTool, "editor", "-i", rpu, "-j", editJSON, "-o", fixed); err != nil {
		return "", err
	}
	return fixed, nil
}

func indexOf(ps []areaPreset, want areaPreset) int {
	for i, p := range ps {
		if p == want {
			return i
		}
	}
	return -1
}

func readLevel5(f rpuFrame) (level5, bool) {
	for _, blk := range f.DM.CMV29.Blocks {
		if raw, ok := blk["Level5"]; ok {
			var l5 level5
			if json.Unmarshal(raw, &l5) == nil {
				return l5, true
			}
		}
	}
	return level5{}, false
}

func writeEditJSON(path string, presets []areaPreset, edits map[string]int) error {
	type jpreset struct {
		ID     int `json:"id"`
		Left   int `json:"left"`
		Right  int `json:"right"`
		Top    int `json:"top"`
		Bottom int `json:"bottom"`
	}
	var jp []jpreset
	for i, p := range presets {
		jp = append(jp, jpreset{ID: i, Left: p.L, Right: p.R, Top: p.T, Bottom: p.B})
	}
	// keep edit ranges in a stable order so output is deterministic
	keys := make([]string, 0, len(edits))
	for k := range edits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	em := map[string]int{}
	for _, k := range keys {
		em[k] = edits[k]
	}
	doc := map[string]any{
		"active_area": map[string]any{
			"presets": jp,
			"edits":   em,
		},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// areaPreset is one active-area crop rectangle (offsets in pixels).
type areaPreset struct{ L, R, T, B int }

func allZero(ps []areaPreset) bool {
	for _, p := range ps {
		if p.L != 0 || p.R != 0 || p.T != 0 || p.B != 0 {
			return false
		}
	}
	return true
}

func scale(v int, m float64) int { return int(float64(v)*m + 0.5) }

// --- helpers ---

// extractHEVC is quiet on success (a spinner is running over it); ffmpeg's
// stderr is only surfaced if the copy actually fails.
func extractHEVC(ctx context.Context, path string, idx int, out string) error {
	cmd, err := tools.Cmd(tools.FFmpeg,
		"-y", "-nostdin", "-loglevel", "error",
		"-i", path,
		"-map", fmt.Sprintf("0:%d", idx),
		"-c", "copy",
		"-bsf:v", "hevc_mp4toannexb",
		"-f", "hevc",
		out,
	)
	if err != nil {
		return err
	}
	var errbuf bytes.Buffer
	cmd.Stderr = &errbuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg extract: %w\n%s", err, errbuf.String())
	}
	return nil
}

// run is quiet on success (a spinner covers the work, and the chains run in
// parallel so interleaved tool output would just be noise). The tool's stderr is
// surfaced only when it actually fails.
func run(ctx context.Context, tool string, args ...string) error {
	cmd, err := tools.Cmd(tool, args...)
	if err != nil {
		return err
	}
	var errbuf bytes.Buffer
	cmd.Stderr = &errbuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w\n%s", filepath.Base(tool), err, errbuf.String())
	}
	return nil
}
