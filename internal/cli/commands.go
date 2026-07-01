package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/720pixel/RedSync/internal/hdr"
	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/naming"
	rsync "github.com/720pixel/RedSync/internal/sync"
	"github.com/720pixel/RedSync/internal/tools"
	"github.com/720pixel/RedSync/internal/ui"
	"github.com/spf13/cobra"
)

// selection is the resolved user intent, however it arrived (flags, quick mode,
// or the interactive picker).
type selection struct {
	video         string
	dv            string // DV source for a hybrid, "" if none (video is the HDR base)
	fromHDR10Plus bool   // build the DV layer from the video source's own HDR10+
	plusFrom      string // HDR10+ source to graft onto the video (video is the DV base) -> DV HDR10+
	audio         []string
	subs          []string
	chapters      string
	keep          bool // also keep the video source's own audio + subs + chapters
	unique        bool // dedupe subtitles to one per language/role across every source
	output        string
	outDir        string
	dryRun        bool

	shift    int  // manual constant delay (ms) applied to every synced source
	shiftSet bool // true when --shift was given, so we skip audio measurement
	jsonOut  bool // emit a machine-readable JSON result to stdout
}

func (s selection) makesHybrid() bool { return s.dv != "" || s.fromHDR10Plus || s.plusFrom != "" }

// dvHasHDR10Base reports whether a Dolby Vision video carries an HDR10-compatible
// base layer - true for profile 8 / 7, false for the single-layer profile 5,
// whose pixels are DV's own color space and can't stand in for HDR10.
func dvHasHDR10Base(h media.HDRInfo) bool {
	return h.HDR10 || h.DVProfile == 8 || h.DVProfile == 7
}

func runSync(ctx context.Context, sel selection) error {
	runStart := time.Now()
	var stages []stageTime
	stage := func(name string, since time.Time) { stages = append(stages, stageTime{name, time.Since(since)}) }

	if sel.video == "" {
		return fmt.Errorf("no video source given (use --video, or a file before --sync)")
	}
	if sel.dv != "" && sel.plusFrom != "" {
		return fmt.Errorf("choose one of --dv (graft DV) or --hdr10plus (graft HDR10+), not both")
	}
	t0 := time.Now()
	paths := dedup(append([]string{sel.video, sel.dv, sel.plusFrom}, append(append(append([]string{}, sel.audio...), sel.subs...), sel.chapters)...))
	files, err := probeAll(ctx, filterEmpty(paths))
	stage("probe sources", t0)
	if err != nil {
		return err
	}
	byPath := map[string]media.File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	ref := byPath[sel.video]
	if len(ref.Video) == 0 {
		return fmt.Errorf("%s has no video track", filepath.Base(sel.video))
	}

	var dvPtr, plusPtr *media.File
	if sel.dv != "" {
		f := byPath[sel.dv]
		dvPtr = &f
	}
	if sel.plusFrom != "" {
		f := byPath[sel.plusFrom]
		plusPtr = &f
	}

	// The hybrid build (extracting + injecting tens of GB of HEVC) and the offset
	// measurement are independent, so for a real run we do them at the same time:
	// one worker builds the hybrid while another measures the audio/sub/chapter
	// sync. They join here before muxing. Intermediates live in a per-run temp dir
	// we delete afterwards.
	var hybridVideo *media.File
	var drifts map[string]rsync.Drift

	if sel.makesHybrid() && !sel.dryRun {
		workDir, werr := newWorkDir()
		if werr != nil {
			return werr
		}
		defer os.RemoveAll(workDir)

		var wg sync.WaitGroup
		var hErr, mErr error
		var hDur, mDur time.Duration
		wg.Add(2)
		go func() {
			defer wg.Done()
			st := time.Now()
			hybridVideo, hErr = buildHybridVideo(ctx, ref, dvPtr, plusPtr, sel.fromHDR10Plus, workDir, false)
			hDur = time.Since(st)
		}()
		go func() {
			defer wg.Done()
			st := time.Now()
			drifts, mErr = measureAll(ctx, ref, byPath, sel, false) // quiet, the spinner is the show
			mDur = time.Since(st)
		}()
		wg.Wait()
		stages = append(stages, stageTime{"build hybrid video", hDur}, stageTime{"measure offsets", mDur})
		if hErr != nil {
			return hErr
		}
		if mErr != nil {
			return mErr
		}
	} else {
		if sel.makesHybrid() {
			t0 = time.Now()
			hybridVideo, err = buildHybridVideo(ctx, ref, dvPtr, plusPtr, sel.fromHDR10Plus, "", true)
			stage("build hybrid video", t0)
			if err != nil {
				return err
			}
		}
		t0 = time.Now()
		drifts, err = measureAll(ctx, ref, byPath, sel, true)
		stage("measure offsets", t0)
		if err != nil {
			return err
		}
	}

	job := assemble(ref, byPath, drifts, sel, hybridVideo)
	// Always route through autoName so --out-dir is honored even when -o gives an
	// explicit filename (naming.Build treats -o as the name and still places it in
	// the chosen directory).
	job.Output = autoName(ref, job, sel)
	reportPlan(job, drifts)

	// In JSON mode a dry run must not run mkvmerge's own stdout print - the
	// command travels inside the JSON instead, keeping stdout a single object.
	if !(sel.dryRun && sel.jsonOut) {
		t0 = time.Now()
		if err = rsync.Run(ctx, job); err != nil {
			return err
		}
		stage("mux", t0)
	}
	reportTimings(stages, time.Since(runStart))

	if sel.jsonOut {
		return emitJSON(buildRunJSON(job, sel, finalRange(ref, sel), stages, time.Since(runStart)))
	}
	return nil
}

// stageTime is how long one phase of a run took, for the timing report.
type stageTime struct {
	name string
	dur  time.Duration
}

// reportTimings prints how long each phase of the run took, so it's obvious
// where the time actually goes (offset probing vs. the hybrid HEVC build vs.
// the final mkvmerge pass).
func reportTimings(stages []stageTime, total time.Duration) {
	ui.Section("timings")
	for _, s := range stages {
		ui.Field(s.name, ui.Muted.Render(fmtDuration(s.dur)))
	}
	ui.Field("total", ui.Accent.Render(fmtDuration(total)))
}

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		return fmt.Sprintf("%dm%02ds", m, s)
	}
}

// newWorkDir makes a unique scratch dir under the cache for one run.
func newWorkDir() (string, error) {
	base, err := tools.CacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(filepath.Dir(base), "work")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, "run-")
}

// runQuick is the two-or-more-file shortcut: the first file is the video, the
// rest hand over their audio, subtitles and chapters (filtered by the -only
// flags). This is `RedSync a.mkv b.mkv --sync`.
func runQuick(ctx context.Context, f *rootFlags, args []string) error {
	files := expand(args)
	if len(files) < 2 {
		return fmt.Errorf("--sync needs at least two files: the video first, then one or more sources")
	}
	srcs := files[1:]

	takeAudio, takeSubs, takeChaps := true, true, true
	switch {
	case f.audioOnly:
		takeSubs, takeChaps = false, false
	case f.subsOnly:
		takeAudio, takeChaps = false, false
	case f.chapsOnly:
		takeAudio, takeSubs = false, false
	}

	sel := selection{
		video: files[0], keep: f.keep, unique: f.unique, output: f.output, outDir: f.outDir,
		dryRun: f.dryRun, shift: f.shift, shiftSet: f.shiftSet, jsonOut: flagJSON,
	}
	if takeAudio {
		sel.audio = srcs
	}
	if takeSubs {
		sel.subs = srcs
	}
	if takeChaps {
		sel.chapters = srcs[0]
	}
	return runSync(ctx, sel)
}

// buildHybridVideo makes the hybrid and returns it probed. Three shapes:
//   - fromHDR10Plus: generate DV from the video source's own HDR10+ metadata
//   - plus != nil:   keep ref's DV video, graft HDR10+ from plus -> DV HDR10+
//   - dv != nil:     graft dv's Dolby Vision onto ref's HDR base -> DV HDR
//
// In dry-run we skip the slow build and just report what it would do.
func buildHybridVideo(ctx context.Context, ref media.File, dv, plus *media.File, fromHDR10Plus bool, work string, dryRun bool) (*media.File, error) {
	switch {
	case fromHDR10Plus:
		hv := ref.Video[0]
		ui.Section("DV from HDR10+")
		ui.Field("source", fmt.Sprintf("%s  %dx%d  %s", filepath.Base(ref.Path), hv.Width, hv.Height, hv.HDR.Range()))
		if !hv.HDR.HDR10Plus {
			ui.Warn("source isn't flagged HDR10+, generating from whatever dynamic metadata is present")
		}
		if dryRun {
			ui.Warn("dry run: skipping the DV generation")
			return nil, nil
		}
		out, err := hdr.FromHDR10Plus(ctx, ref, work, "")
		if err != nil {
			return nil, err
		}
		hf, err := media.Probe(ctx, out)
		return &hf, err

	case plus != nil:
		// Keep the Dolby Vision video from ref and borrow the HDR10+ metadata from
		// plus. The DV RPU - including its active-area (L5) crop - rides along in
		// the DV stream untouched, and HDR10+ carries no active area of its own, so
		// cropping is inherited from the DV source exactly. The one thing worth a
		// warning is a framing mismatch, which would mean the borrowed tone-mapping
		// was graded against a differently-cropped picture.
		dvv, pv := ref.Video[0], plus.Video[0]
		ui.Section("DV + HDR10+ hybrid")
		ui.Field("dv base", fmt.Sprintf("%s  %dx%d  %s  (profile %d)", filepath.Base(ref.Path), dvv.Width, dvv.Height, dvv.HDR.Range(), dvv.HDR.DVProfile))
		ui.Field("hdr10+ from", fmt.Sprintf("%s  %dx%d  %s", filepath.Base(plus.Path), pv.Width, pv.Height, pv.HDR.Range()))
		if !dvv.HDR.DV {
			ui.Warn(filepath.Base(ref.Path) + " isn't flagged Dolby Vision; the result may not be a valid DV base")
		}
		// Profile 5 is single-layer DV with no HDR10 base - its pixels are DV's
		// IPT space, not HDR10. Keeping them and stamping HDR10+ on top makes a file
		// that renders wrong on HDR10+/HDR10 displays. The valid build for that case
		// keeps the HDR10+ video and grafts the DV onto it instead.
		if !dvHasHDR10Base(dvv.HDR) {
			return nil, fmt.Errorf(
				"%s is Dolby Vision profile %d (single-layer, no HDR10 base), so keeping its video\n"+
					"    can't produce a valid HDR10+ file. Build it the other way round - keep the HDR10+\n"+
					"    video and graft this DV onto it:\n"+
					"      RedSync hybrid --hdr %q --dv %q",
				filepath.Base(ref.Path), dvv.HDR.DVProfile, filepath.Base(plus.Path), filepath.Base(ref.Path))
		}
		if dvv.Width != pv.Width || dvv.Height != pv.Height {
			ui.Warn(fmt.Sprintf("frame size differs (DV %dx%d vs HDR10+ %dx%d): the DV active-area crop is kept, but the HDR10+ tone-mapping was graded on the other framing",
				dvv.Width, dvv.Height, pv.Width, pv.Height))
		} else {
			ui.Field("crop", "DV active area preserved from the DV source")
		}
		if dryRun {
			ui.Warn("dry run: skipping the HDR10+ graft (it would extract + inject the HEVC)")
			return nil, nil
		}
		out, err := hdr.AddHDR10Plus(ctx, ref, *plus, work, "")
		if err != nil {
			return nil, err
		}
		hf, err := media.Probe(ctx, out)
		return &hf, err

	default:
		if dv == nil || len(dv.Video) == 0 {
			return nil, fmt.Errorf("no Dolby Vision source given to take the DV layer from")
		}
		hv, dvv := ref.Video[0], dv.Video[0]
		geo := hdr.ComputeGeometry(hv.Width, hv.Height, dvv.Width, dvv.Height)
		ui.Section("DV+HDR hybrid")
		ui.Field("hdr base", fmt.Sprintf("%dx%d  %s", hv.Width, hv.Height, hv.HDR.Range()))
		ui.Field("dv layer", fmt.Sprintf("%dx%d  DV profile %d", dvv.Width, dvv.Height, dvv.HDR.DVProfile))
		// When the DV layer is taller than the base, its bars are cropped away to
		// match, so the active area is zeroed rather than fitted (see BuildHybrid).
		if dvv.Height > hv.Height {
			ui.Field("auto crop", "none (DV layer taller than base; its bars cropped to match)")
		} else {
			ui.Field("auto crop", fmt.Sprintf("top %d  bottom %d  left %d  right %d", geo.Top, geo.Bottom, geo.Left, geo.Right))
		}
		if dryRun {
			ui.Warn("dry run: skipping the hybrid build (it would extract + inject the HEVC)")
			return nil, nil
		}
		out, err := hdr.BuildHybrid(ctx, hdr.HybridJob{HDRSrc: ref, DVSrc: *dv, WorkDir: work})
		if err != nil {
			return nil, err
		}
		hf, err := media.Probe(ctx, out)
		return &hf, err
	}
}

// manualDrifts skips measurement entirely and pins every synced source to the
// user's --shift constant. The reference (the video source) stays at zero.
func manualDrifts(ref media.File, sel selection) map[string]rsync.Drift {
	srcs := filterEmpty(dedup(append(append(append([]string{}, sel.audio...), sel.subs...), sel.chapters)))
	out := map[string]rsync.Drift{}
	for _, p := range srcs {
		if p == ref.Path {
			continue
		}
		out[p] = rsync.Drift{DelayMS: sel.shift}
	}
	return out
}

// measureAll runs the offset probes for every contributing source concurrently.
// announce controls the per-source log line (off when it runs behind a spinner).
// With --shift the audio probes are skipped for the caller's fixed delay.
func measureAll(ctx context.Context, ref media.File, byPath map[string]media.File, sel selection, announce bool) (map[string]rsync.Drift, error) {
	if sel.shiftSet {
		if announce {
			ui.Step(fmt.Sprintf("manual shift: %dms (skipping audio measurement)", sel.shift))
		}
		return manualDrifts(ref, sel), nil
	}
	srcs := filterEmpty(dedup(append(append(append([]string{}, sel.audio...), sel.subs...), sel.chapters)))

	type res struct {
		path string
		d    rsync.Drift
		err  error
	}
	ch := make(chan res, len(srcs))
	n := 0
	for _, p := range srcs {
		if p == ref.Path {
			continue // reference syncs to itself
		}
		n++
		go func(p string) {
			if announce {
				ui.Step("measuring offset: " + filepath.Base(p))
			}
			d, err := rsync.Measure(ctx, ref, byPath[p], ref.Duration)
			ch <- res{p, d, err}
		}(p)
	}
	out := map[string]rsync.Drift{}
	for i := 0; i < n; i++ {
		r := <-ch
		if r.err != nil {
			return nil, fmt.Errorf("measure %s: %w", filepath.Base(r.path), r.err)
		}
		out[r.path] = r.d
	}
	return out, nil
}

// assemble groups the chosen sources into per-file contributions. When a hybrid
// was built, its HEVC is a separate video-only contribution - the reference mkv
// can still hand over its own audio/subs/chapters, which must come from the mkv
// (that has those tracks), not from the video-only hybrid stream.
func assemble(ref media.File, byPath map[string]media.File, drifts map[string]rsync.Drift, sel selection, hybridVideo *media.File) rsync.Job {
	contribs := map[string]*rsync.Contribution{}
	order := []string{}
	get := func(path string) *rsync.Contribution {
		c, ok := contribs[path]
		if !ok {
			d := drifts[path]
			if path == ref.Path {
				d = rsync.Drift{} // the reference syncs to itself
			}
			c = &rsync.Contribution{File: byPath[path], Drift: d}
			contribs[path] = c
			order = append(order, path)
		}
		return c
	}

	// addSubs hands a source's subtitle tracks to a contribution. With --unique
	// it also filters them: subSeen tracks which (language, forced, SDH, title)
	// roles have already been taken, in the order sources are processed, so a
	// source only contributes the tracks no earlier source already covered -
	// e.g. a smaller source's FR-CA survives even when a richer source that
	// only has FR-FR is processed first.
	subSeen := map[string]bool{}
	addSubs := func(c *rsync.Contribution, tracks []media.Track) {
		if !sel.unique {
			c.Subs = append(c.Subs, tracks...)
			return
		}
		for _, t := range tracks {
			key := subRoleKey(t)
			if subSeen[key] {
				continue
			}
			subSeen[key] = true
			c.Subs = append(c.Subs, t)
		}
	}

	// track providers (audio / subtitles / chapters)
	if sel.keep {
		c := get(ref.Path)
		c.Audio = append(c.Audio, ref.Audio...)
		addSubs(c, ref.Subs)
		if sel.chapters == "" && ref.HasChapters {
			c.Chapters = true
		}
	}
	for _, p := range sel.audio {
		get(p).Audio = append(get(p).Audio, byPath[p].Audio...)
	}
	for _, p := range sel.subs {
		addSubs(get(p), byPath[p].Subs)
	}
	if sel.chapters != "" {
		get(sel.chapters).Chapters = true
	}

	job := rsync.Job{Output: sel.output, DryRun: sel.dryRun}

	// video provider goes first so it ends up as track 0.
	if hybridVideo != nil {
		vt := ref.Video[0] // keep the reference fps; the raw HEVC carries none
		if len(hybridVideo.Video) > 0 {
			vt.Index = hybridVideo.Video[0].Index
		}
		job.Contribs = append(job.Contribs, rsync.Contribution{
			File: *hybridVideo, Video: []media.Track{vt}, Drift: rsync.Drift{}, FPSFix: true,
		})
		for _, p := range order {
			job.Contribs = append(job.Contribs, *contribs[p])
		}
	} else {
		vc := get(ref.Path)
		vc.Video = []media.Track{ref.Video[0]}
		job.Contribs = append(job.Contribs, *contribs[ref.Path])
		for _, p := range order {
			if p != ref.Path {
				job.Contribs = append(job.Contribs, *contribs[p])
			}
		}
	}
	return job
}

// finalRange is the dynamic-range tag the output will carry once the hybrid is
// built: the video source's own range, upgraded for whatever DV/HDR10+ graft the
// selection asks for.
func finalRange(ref media.File, sel selection) string {
	rng := ""
	if len(ref.Video) > 0 {
		rng = ref.Video[0].HDR.Range()
	}
	switch {
	case sel.plusFrom != "":
		// keeping a DV base and grafting HDR10+ always lands on DV HDR10+
		rng = "DV HDR10+"
	case sel.makesHybrid():
		rng = upgradeToDV(rng) // HDR10 -> "DV HDR10", HDR10+ -> "DV HDR10+"
	}
	return rng
}

func autoName(ref media.File, job rsync.Job, sel selection) string {
	var primaryAudio *media.Track
	for _, c := range job.Contribs {
		if len(c.Audio) > 0 {
			primaryAudio = &c.Audio[0]
			break
		}
	}
	return naming.Build(naming.Plan{
		VideoPath: ref.Path,
		Audio:     primaryAudio,
		Range:     finalRange(ref, sel),
		Override:  sel.output,
		OutDir:    sel.outDir,
	})
}

// upgradeToDV turns an HDR range tag into its DV-hybrid form.
func upgradeToDV(rng string) string {
	switch rng {
	case "HDR10":
		return "DV HDR10"
	case "HDR10+":
		return "DV HDR10+"
	case "HLG":
		return "DV HLG"
	case "", "SDR":
		return "DV"
	}
	if len(rng) >= 2 && rng[:2] == "DV" {
		return rng
	}
	return "DV " + rng
}

func reportPlan(job rsync.Job, drifts map[string]rsync.Drift) {
	ui.Section("sync plan")
	for _, c := range job.Contribs {
		var role []string
		if len(c.Video) > 0 {
			role = append(role, "video")
		}
		if len(c.Audio) > 0 {
			role = append(role, fmt.Sprintf("%d audio", len(c.Audio)))
		}
		if len(c.Subs) > 0 {
			role = append(role, fmt.Sprintf("%d subs", len(c.Subs)))
		}
		if c.Chapters {
			role = append(role, "chapters")
		}
		ui.Field("source", fmt.Sprintf("%-34s %s", filepath.Base(c.File.Path), ui.Muted.Render(join(role))))
		if c.Drift.DelayMS != 0 || c.Drift.Linear != "" {
			msg := fmt.Sprintf("delay %dms", c.Drift.DelayMS)
			if c.Drift.FPSStretch {
				msg += fmt.Sprintf("  fps stretch %s", c.Drift.Linear)
				ui.Warn(fmt.Sprintf("%s: frame-rate mismatch corrected (%s)", filepath.Base(c.File.Path), c.Drift.Linear))
			}
			ui.Field("", ui.Accent.Render(msg))
		}
	}
	ui.Field("output", ui.Title.Render(filepath.Base(job.Output)))
}

// --- hybrid (standalone, produces a finished mkv) ---

func hybridCmd() *cobra.Command {
	var (
		hdrPath   string
		dvPath    string
		plusPath  string
		output    string
		outDir    string
		keep      bool
		hevcOnly  bool
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "hybrid (--hdr FILE --dv FILE | --dv FILE --hdr10plus FILE | --hdr10plus FILE)",
		Short: "Build a DV+HDR / DV+HDR10+ hybrid, or turn an HDR10+ source into DV HDR10+",
		Long: "Build a finished hybrid mkv.\n" +
			"  --hdr H --dv D        graft the Dolby Vision from D onto the HDR10/HDR10+ video H\n" +
			"  --dv D --hdr10plus S  keep D's Dolby Vision video and graft S's HDR10+ metadata -> DV HDR10+\n" +
			"  --hdr10plus S         generate DV from S's own HDR10+ metadata -> DV HDR10+\n" +
			"By default the video source's audio, subtitles and chapters are kept.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			// raw stream only, no remux
			if hevcOnly {
				return hybridHEVCOnly(ctx, hdrPath, dvPath, plusPath, output)
			}

			switch {
			case dvPath != "" && plusPath != "":
				// keep the DV video, graft the separate HDR10+ metadata onto it
				return runSync(ctx, selection{
					video: dvPath, plusFrom: plusPath, keep: keep,
					output: output, outDir: outDir, dryRun: dryRun, jsonOut: flagJSON,
				})
			case plusPath != "":
				return runSync(ctx, selection{
					video: plusPath, fromHDR10Plus: true, keep: keep,
					output: output, outDir: outDir, dryRun: dryRun, jsonOut: flagJSON,
				})
			case hdrPath != "" && dvPath != "":
				return runSync(ctx, selection{
					video: hdrPath, dv: dvPath, keep: keep,
					output: output, outDir: outDir, dryRun: dryRun, jsonOut: flagJSON,
				})
			default:
				return fmt.Errorf("give --hdr and --dv, --dv and --hdr10plus, or --hdr10plus alone")
			}
		},
	}
	f := cmd.Flags()
	f.StringVar(&hdrPath, "hdr", "", "HDR10/HDR10+ source (its video is the base layer kept)")
	f.StringVar(&dvPath, "dv", "", "Dolby Vision source (with --hdr its RPU is grafted; with --hdr10plus its video is kept)")
	f.StringVar(&plusPath, "hdr10plus", "", "HDR10+ source: alone -> DV HDR10+; with --dv its HDR10+ is grafted onto the DV video")
	f.StringVarP(&output, "output", "o", "", "output filename (auto-named if omitted)")
	f.StringVar(&outDir, "out-dir", "", "directory to write the result into")
	f.BoolVar(&keep, "keep", true, "keep the base source's audio, subtitles and chapters")
	f.BoolVar(&hevcOnly, "hevc-only", false, "write just the hybrid HEVC stream, no remux")
	f.BoolVar(&dryRun, "dry-run", false, "print the plan without writing anything")
	return cmd
}

// hybridHEVCOnly writes only the elementary stream, for when you want to mux it
// yourself. With --json it prints {"hevc": "<path>"} to stdout.
func hybridHEVCOnly(ctx context.Context, hdrPath, dvPath, plusPath, output string) error {
	work, _ := tools.CacheDir()
	work = filepath.Join(filepath.Dir(work), "work")

	done := func(label, out string) error {
		if flagJSON {
			return emitJSON(map[string]string{"hevc": out})
		}
		ui.OK(label + ": " + out)
		return nil
	}

	switch {
	case dvPath != "" && plusPath != "":
		df, err := media.Probe(ctx, dvPath)
		if err != nil {
			return err
		}
		pf, err := media.Probe(ctx, plusPath)
		if err != nil {
			return err
		}
		out, err := hdr.AddHDR10Plus(ctx, df, pf, work, output)
		if err != nil {
			return err
		}
		return done("DV HDR10+ stream", out)
	case plusPath != "":
		src, err := media.Probe(ctx, plusPath)
		if err != nil {
			return err
		}
		out, err := hdr.FromHDR10Plus(ctx, src, work, output)
		if err != nil {
			return err
		}
		return done("DV HDR10+ stream", out)
	case hdrPath != "" && dvPath != "":
		hf, err := media.Probe(ctx, hdrPath)
		if err != nil {
			return err
		}
		df, err := media.Probe(ctx, dvPath)
		if err != nil {
			return err
		}
		out, err := hdr.BuildHybrid(ctx, hdr.HybridJob{HDRSrc: hf, DVSrc: df, WorkDir: work, OutHEVC: output})
		if err != nil {
			return err
		}
		return done("hybrid HEVC stream", out)
	}
	return fmt.Errorf("give --hdr and --dv, --dv and --hdr10plus, or --hdr10plus alone")
}

// --- doctor ---

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that the external tools RedSync needs are available",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flagJSON {
				return doctorJSON()
			}
			ui.Section("tool check")
			missing := 0
			for _, t := range tools.Required() {
				path, ok := tools.Locate(t)
				switch {
				case ok:
					where := path
					if tools.Bundled(t) {
						where = "bundled"
					}
					ui.OK(fmt.Sprintf("%-14s %s", t, ui.Muted.Render(where)))
				case t == tools.FFmpeg || t == tools.FFprobe:
					ui.Warn(fmt.Sprintf("%-14s not found, will auto-fetch on first run", t))
				default:
					missing++
					ui.Err(fmt.Sprintf("%-14s %s", t, ui.Muted.Render(tools.Hint(t))))
				}
			}
			if missing > 0 {
				return fmt.Errorf("%d required tool(s) missing", missing)
			}
			ui.OK("all set")
			return nil
		},
	}
}

// doctorJSON is `doctor` for scripts: the same check emitted as JSON. ffmpeg and
// ffprobe don't count as missing since they auto-fetch on first run.
func doctorJSON() error {
	rep := jsonDoctor{OK: true}
	for _, t := range tools.Required() {
		path, ok := tools.Locate(t)
		jt := jsonTooled{Name: t, Found: ok, Bundled: tools.Bundled(t)}
		if ok {
			jt.Path = path
			if jt.Bundled {
				jt.Path = "bundled"
			}
		} else {
			jt.Hint = tools.Hint(t)
			if t != tools.FFmpeg && t != tools.FFprobe {
				rep.OK = false
			}
		}
		rep.Tools = append(rep.Tools, jt)
	}
	if err := emitJSON(rep); err != nil {
		return err
	}
	if !rep.OK {
		return fmt.Errorf("required tool(s) missing")
	}
	return nil
}

// --- small slice helpers ---

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func filterEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
