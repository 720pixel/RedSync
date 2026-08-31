package cli

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/subtitle"
	rsync "github.com/720pixel/RedSync/internal/sync"
	"github.com/720pixel/RedSync/internal/timeline"
	"github.com/720pixel/RedSync/internal/ui"
	"github.com/spf13/cobra"
)

type standaloneFlags struct {
	output         string
	outDir         string
	format         string
	codec          string
	bitrate        string
	channels       int
	sampleRate     int
	language       string
	dryRun         bool
	overwrite      bool
	force          bool
	verify         bool
	detectGaps     bool
	shift          int
	factor         float64
	maxOffset      float64
	minScore       float64
	minGap         float64
	maxSegments    int
	referenceTrack int
	targetTrack    int
}

type standaloneResult struct {
	SchemaVersion int                     `json:"schema_version"`
	Mode          string                  `json:"mode"`
	Reference     string                  `json:"reference"`
	Target        string                  `json:"target"`
	Output        string                  `json:"output"`
	Language      string                  `json:"language,omitempty"`
	DryRun        bool                    `json:"dry_run"`
	SyncMS        int                     `json:"sync_ms"`
	Scale         float64                 `json:"scale"`
	DriftPPM      float64                 `json:"drift_ppm"`
	FPSConversion string                  `json:"fps_conversion"`
	Score         float64                 `json:"score"`
	OriginalScore float64                 `json:"original_score,omitempty"`
	Samples       int                     `json:"samples"`
	ResidualMS    int                     `json:"residual_ms"`
	Segments      []timeline.Segment      `json:"segments"`
	Gaps          []timeline.Gap          `json:"gaps"`
	Verification  *standaloneVerification `json:"verification,omitempty"`
}

type standaloneVerification struct {
	Passed                   bool           `json:"passed"`
	SyncMS                   int            `json:"sync_ms"`
	Scale                    float64        `json:"scale"`
	DriftPPM                 float64        `json:"drift_ppm"`
	FPSConversion            string         `json:"fps_conversion"`
	Score                    float64        `json:"score"`
	Samples                  int            `json:"samples"`
	ResidualMS               int            `json:"residual_ms"`
	ReferenceDurationSeconds float64        `json:"reference_duration_seconds"`
	OutputDurationSeconds    float64        `json:"output_duration_seconds"`
	DurationDeltaMS          int            `json:"duration_delta_ms"`
	Gaps                     []timeline.Gap `json:"gaps"`
}

func standaloneSyncCmd() *cobra.Command {
	f := &standaloneFlags{referenceTrack: -1, targetTrack: -1, verify: true, detectGaps: true, maxSegments: 8}
	cmd := &cobra.Command{
		Use:   "sync <reference> <target...>",
		Short: "Create standalone synced audio or subtitle files",
		Long: `Align standalone files without a video container.

The first path is the correctly timed reference. Every remaining file is a
target. Targets may also be directories, which are scanned recursively for
batch processing. Audio is matched from multiple spectral probes and rendered
through FFmpeg; subtitles are aligned from language-independent cue activity.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			shiftSet := cmd.Flags().Changed("shift")
			factorSet := cmd.Flags().Changed("factor")
			return runStandaloneSync(cmd.Context(), args[0], args[1:], f, shiftSet, factorSet)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.output, "output", "o", "", "output path (only valid with one target)")
	fl.StringVar(&f.outDir, "out-dir", "", "directory for synced files (defaults beside each target)")
	fl.StringVar(&f.format, "format", "", "output extension for every target, e.g. vtt, mka, m4a or mp3")
	fl.StringVar(&f.codec, "codec", "", "FFmpeg audio encoder override (audio mode only)")
	fl.StringVar(&f.bitrate, "bitrate", "", "audio bitrate override, e.g. 96k (audio mode only)")
	fl.IntVar(&f.channels, "channels", 0, "audio channel count override (audio mode only)")
	fl.IntVar(&f.sampleRate, "sample-rate", 0, "audio sample rate override in Hz (audio mode only)")
	fl.StringVar(&f.language, "language", "", "output audio language tag override")
	fl.BoolVar(&f.dryRun, "dry-run", false, "measure and show the plan without writing files")
	fl.BoolVar(&f.overwrite, "overwrite", false, "replace an existing synced output")
	fl.BoolVar(&f.force, "force", false, "accept an alignment below the confidence threshold")
	fl.BoolVar(&f.verify, "verify", true, "re-measure each finished output and report the residual sync")
	fl.BoolVar(&f.detectGaps, "detect-gaps", true, "detect and repair internal timeline discontinuities")
	fl.IntVar(&f.shift, "shift", 0, "manual constant correction in milliseconds (negative advances the target)")
	fl.Float64Var(&f.factor, "factor", 1, "manual target timestamp multiplier (use with --shift for known drift)")
	fl.Float64Var(&f.maxOffset, "max-offset", 300, "largest expected absolute offset in seconds")
	fl.Float64Var(&f.minScore, "min-score", 0, "minimum confidence (defaults differ for audio and subtitles)")
	fl.Float64Var(&f.minGap, "min-gap", 0.35, "smallest internal discontinuity to repair, in seconds")
	fl.IntVar(&f.maxSegments, "max-segments", 8, "maximum piecewise timeline segments")
	fl.IntVar(&f.referenceTrack, "reference-track", -1, "reference audio stream index (default: first audio stream)")
	fl.IntVar(&f.targetTrack, "target-track", -1, "target audio stream index (default: first audio stream)")
	return cmd
}

func runStandaloneSync(ctx context.Context, reference string, targetArgs []string, f *standaloneFlags, shiftSet, factorSet bool) error {
	if f.channels < 0 || f.sampleRate < 0 {
		return fmt.Errorf("--channels and --sample-rate cannot be negative")
	}
	if f.minGap <= 0 {
		return fmt.Errorf("--min-gap must be greater than zero")
	}
	if f.maxSegments < 1 || f.maxSegments > 32 {
		return fmt.Errorf("--max-segments must be between 1 and 32")
	}
	info, err := os.Stat(reference)
	if err != nil {
		return fmt.Errorf("reference: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("reference must be a file, not a directory")
	}
	mode := "audio"
	if subtitle.IsTextExtension(reference) {
		mode = "subtitles"
	} else if probed, probeErr := media.Probe(ctx, reference); probeErr == nil && len(probed.Audio) == 0 && len(probed.Subs) > 0 {
		mode = "subtitles"
	}
	targets, err := expandStandaloneTargets(targetArgs, mode, reference)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no %s targets found", mode)
	}
	if f.output != "" && len(targets) != 1 {
		return fmt.Errorf("--output can only be used with one target; use --out-dir for a batch")
	}

	if mode == "subtitles" {
		return syncSubtitleTargets(ctx, reference, targets, f, shiftSet, factorSet)
	}
	return syncAudioTargets(ctx, reference, targets, f, shiftSet, factorSet)
}

func syncAudioTargets(ctx context.Context, reference string, targets []string, f *standaloneFlags, shiftSet, factorSet bool) error {
	ref, err := media.Probe(ctx, reference)
	if err != nil {
		return err
	}
	refTrack, err := chooseAudioTrack(ref, f.referenceTrack)
	if err != nil {
		return fmt.Errorf("reference: %w", err)
	}
	var results []standaloneResult
	if len(targets) > 1 {
		ui.Section("audio batch")
		ui.Field("targets", fmt.Sprintf("%d files", len(targets)))
	}
	for _, targetPath := range targets {
		target, err := media.Probe(ctx, targetPath)
		if err != nil {
			return fmt.Errorf("target %s: %w", filepath.Base(targetPath), err)
		}
		targetTrack, err := chooseAudioTrack(target, f.targetTrack)
		if err != nil {
			return fmt.Errorf("target %s: %w", filepath.Base(targetPath), err)
		}
		output := standaloneOutput(targetPath, f, "audio")
		ui.Section("audio sync")
		ui.Field("reference", filepath.Base(reference))
		ui.Field("target", filepath.Base(targetPath))

		var drift rsync.Drift
		if shiftSet || factorSet {
			drift.DelayMS = f.shift
			drift.Scale = f.factor
			if math.Abs(f.factor-1) > 0.0000005 {
				drift.Linear = fmt.Sprintf("%.9f", f.factor)
				drift.FPSStretch = true
			}
		} else {
			ui.Step("measuring spectral anchors across the runtime")
			minScore := audioMinScore(f.minScore)
			if f.force {
				minScore = 0.5
			}
			drift, err = rsync.MeasureAudio(ctx, ref, target, refTrack.Index, targetTrack.Index, rsync.MeasureOptions{
				MaxOffsetSeconds: f.maxOffset,
				MinScore:         minScore,
				MinGapSeconds:    f.minGap,
				MaxSegments:      f.maxSegments,
				DisablePiecewise: !f.detectGaps,
			})
			if err != nil {
				return err
			}
		}
		ui.Field("sync (ms)", fmt.Sprintf("%+d", drift.DelayMS))
		ui.Field("scale", fmt.Sprintf("%.9f", drift.Factor()))
		ui.Field("FPS / timing", timingDescription(drift.Factor()))
		if drift.Score > 0 {
			ui.Field("confidence", fmt.Sprintf("%.2f (%d anchors, %dms residual)", drift.Score, drift.Samples, drift.ResidualMS))
		}
		reportGaps(drift.Gaps)
		ui.Field("output", output)
		var verification *standaloneVerification
		if !f.dryRun {
			ui.Step("rendering synced audio")
			if err := rsync.RenderAudio(ctx, target, targetTrack, drift, ref.Duration, output, rsync.AudioRenderOptions{
				Codec: f.codec, BitRate: f.bitrate, Channels: f.channels, SampleRate: f.sampleRate,
				Language: f.language, Overwrite: f.overwrite,
			}); err != nil {
				return err
			}
			if f.verify {
				ui.Step("verifying the finished audio")
				verification, err = verifyAudioOutput(ctx, ref, refTrack, output, f)
				if err != nil {
					return fmt.Errorf("verify %s: %w", filepath.Base(output), err)
				}
				reportVerification(verification)
			}
		}
		language := f.language
		if language == "" {
			language = targetTrack.Language
		}
		results = append(results, standaloneResult{
			SchemaVersion: 2, Mode: "audio", Reference: reference, Target: targetPath, Output: output,
			Language: language, DryRun: f.dryRun,
			SyncMS: drift.DelayMS, Scale: drift.Factor(), DriftPPM: (drift.Factor() - 1) * 1_000_000,
			FPSConversion: timingDescription(drift.Factor()), Score: drift.Score,
			Samples: drift.Samples, ResidualMS: drift.ResidualMS, Segments: nonNilSegments(drift.Segments),
			Gaps: nonNilGaps(drift.Gaps), Verification: verification,
		})
	}
	if flagJSON {
		return emitJSON(results)
	}
	return nil
}

func syncSubtitleTargets(ctx context.Context, reference string, targets []string, f *standaloneFlags, shiftSet, factorSet bool) error {
	refCues, err := subtitle.Read(ctx, reference)
	if err != nil {
		return fmt.Errorf("reference subtitle: %w", err)
	}
	var results []standaloneResult
	if len(targets) > 1 {
		ui.Section("subtitle batch")
		ui.Field("targets", fmt.Sprintf("%d files", len(targets)))
	}
	for _, targetPath := range targets {
		targetCues, err := subtitle.Read(ctx, targetPath)
		if err != nil {
			return fmt.Errorf("target %s: %w", filepath.Base(targetPath), err)
		}
		output := standaloneOutput(targetPath, f, "subtitles")
		ui.Section("subtitle sync")
		ui.Field("reference", filepath.Base(reference))
		ui.Field("target", filepath.Base(targetPath))
		var alignment subtitle.Alignment
		if shiftSet || factorSet {
			alignment = subtitle.Alignment{OffsetMS: f.shift, Scale: f.factor, ReferenceCues: len(refCues), TargetCues: len(targetCues)}
		} else {
			minScore := subtitleMinScore(f.minScore)
			if f.force {
				minScore = 0.01
			}
			alignment, err = subtitle.Align(refCues, targetCues, subtitle.AlignOptions{
				MaxOffsetSeconds: f.maxOffset,
				MinScore:         minScore,
				MinGapSeconds:    f.minGap,
				MaxSegments:      f.maxSegments,
				DisablePiecewise: !f.detectGaps,
			})
			if err != nil {
				return fmt.Errorf("align %s: %w", filepath.Base(targetPath), err)
			}
		}
		ui.Field("sync (ms)", fmt.Sprintf("%+d", alignment.OffsetMS))
		ui.Field("scale", fmt.Sprintf("%.9f", alignment.Scale))
		ui.Field("FPS / timing", timingDescription(alignment.Scale))
		if alignment.Score > 0 {
			ui.Field("match", fmt.Sprintf("%.3f (before %.3f, %dms residual)", alignment.Score, alignment.OriginalScore, alignment.ResidualMS))
		}
		reportGaps(alignment.Gaps)
		ui.Field("output", output)
		var verification *standaloneVerification
		if !f.dryRun {
			synced := subtitle.Apply(targetCues, alignment)
			if err := subtitle.Write(ctx, output, synced, f.overwrite); err != nil {
				return err
			}
			if f.verify {
				ui.Step("verifying the finished subtitles")
				verification, err = verifySubtitleOutput(ctx, refCues, output, f)
				if err != nil {
					return fmt.Errorf("verify %s: %w", filepath.Base(output), err)
				}
				reportVerification(verification)
			}
		}
		results = append(results, standaloneResult{
			SchemaVersion: 2, Mode: "subtitles", Reference: reference, Target: targetPath, Output: output, DryRun: f.dryRun,
			SyncMS: alignment.OffsetMS, Scale: alignment.Scale, DriftPPM: (alignment.Scale - 1) * 1_000_000,
			FPSConversion: timingDescription(alignment.Scale), Score: alignment.Score, OriginalScore: alignment.OriginalScore,
			Samples: alignment.Samples, ResidualMS: alignment.ResidualMS, Segments: nonNilSegments(alignment.Segments),
			Gaps: nonNilGaps(alignment.Gaps), Verification: verification,
		})
	}
	if flagJSON {
		return emitJSON(results)
	}
	return nil
}

func verifyAudioOutput(ctx context.Context, ref media.File, refTrack media.Track, output string, f *standaloneFlags) (*standaloneVerification, error) {
	finished, err := media.Probe(ctx, output)
	if err != nil {
		return nil, err
	}
	track, err := chooseAudioTrack(finished, -1)
	if err != nil {
		return nil, err
	}
	drift, err := rsync.MeasureAudio(ctx, ref, finished, refTrack.Index, track.Index, rsync.MeasureOptions{
		MaxOffsetSeconds: math.Min(f.maxOffset, 30), MinScore: audioMinScore(f.minScore),
		MinGapSeconds: f.minGap, MaxSegments: f.maxSegments,
	})
	if err != nil {
		return nil, err
	}
	ppm := (drift.Factor() - 1) * 1_000_000
	durationDelta := int(math.Round((finished.Duration - ref.Duration) * 1000))
	return &standaloneVerification{
		Passed: absInt(drift.DelayMS) <= 80 && math.Abs(ppm) <= 50 && drift.ResidualMS <= 100 && absInt(durationDelta) <= 100 && len(drift.Gaps) == 0,
		SyncMS: drift.DelayMS, Scale: drift.Factor(), DriftPPM: ppm,
		FPSConversion: timingDescription(drift.Factor()), Score: drift.Score,
		Samples: drift.Samples, ResidualMS: drift.ResidualMS,
		ReferenceDurationSeconds: ref.Duration, OutputDurationSeconds: finished.Duration, DurationDeltaMS: durationDelta,
		Gaps: nonNilGaps(drift.Gaps),
	}, nil
}

func verifySubtitleOutput(ctx context.Context, reference []subtitle.Cue, output string, f *standaloneFlags) (*standaloneVerification, error) {
	finished, err := subtitle.Read(ctx, output)
	if err != nil {
		return nil, err
	}
	a, err := subtitle.Align(reference, finished, subtitle.AlignOptions{
		MaxOffsetSeconds: math.Min(f.maxOffset, 30), MinScore: subtitleMinScore(f.minScore),
		MinGapSeconds: f.minGap, MaxSegments: f.maxSegments,
	})
	if err != nil {
		return nil, err
	}
	ppm := (a.Scale - 1) * 1_000_000
	_, referenceEnd := subtitleCueBounds(reference)
	_, outputEnd := subtitleCueBounds(finished)
	durationDelta := int(math.Round((outputEnd - referenceEnd) * 1000))
	return &standaloneVerification{
		Passed: absInt(a.OffsetMS) <= 80 && math.Abs(ppm) <= 50 && a.ResidualMS <= 100 && len(a.Gaps) == 0,
		SyncMS: a.OffsetMS, Scale: a.Scale, DriftPPM: ppm,
		FPSConversion: timingDescription(a.Scale), Score: a.Score,
		Samples: a.Samples, ResidualMS: a.ResidualMS,
		ReferenceDurationSeconds: referenceEnd, OutputDurationSeconds: outputEnd, DurationDeltaMS: durationDelta,
		Gaps: nonNilGaps(a.Gaps),
	}, nil
}

func subtitleCueBounds(cues []subtitle.Cue) (float64, float64) {
	if len(cues) == 0 {
		return 0, 0
	}
	first := float64(cues[0].Start) / float64(time.Second)
	last := float64(cues[0].End) / float64(time.Second)
	for _, cue := range cues[1:] {
		first = math.Min(first, float64(cue.Start)/float64(time.Second))
		last = math.Max(last, float64(cue.End)/float64(time.Second))
	}
	return first, last
}

func reportGaps(gaps []timeline.Gap) {
	for i, gap := range gaps {
		action := "insert silence"
		if gap.Action == "remove_target" {
			action = "remove target section"
		}
		ui.Field(fmt.Sprintf("gap %d", i+1), fmt.Sprintf("%s: %dms at target %dms", action, gap.DurationMS, gap.TargetAtMS))
	}
}

func reportVerification(v *standaloneVerification) {
	if v == nil {
		return
	}
	status := "passed"
	if !v.Passed {
		status = "needs review"
	}
	ui.Field("verification", fmt.Sprintf("%s (%+dms, %+.0f ppm, %dms residual, %d gaps)", status, v.SyncMS, v.DriftPPM, v.ResidualMS, len(v.Gaps)))
}

func nonNilSegments(v []timeline.Segment) []timeline.Segment {
	if v == nil {
		return []timeline.Segment{}
	}
	return v
}

func nonNilGaps(v []timeline.Gap) []timeline.Gap {
	if v == nil {
		return []timeline.Gap{}
	}
	return v
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func chooseAudioTrack(f media.File, wanted int) (media.Track, error) {
	if wanted < 0 {
		if len(f.Audio) == 0 {
			return media.Track{}, fmt.Errorf("no audio stream")
		}
		return f.Audio[0], nil
	}
	for _, track := range f.Audio {
		if track.Index == wanted {
			return track, nil
		}
	}
	return media.Track{}, fmt.Errorf("audio stream %d not found", wanted)
}

var standaloneAudioExt = map[string]bool{
	".aac": true, ".ac3": true, ".eac3": true, ".ec3": true, ".m4a": true,
	".mp3": true, ".flac": true, ".wav": true, ".wave": true, ".ogg": true,
	".opus": true, ".mka": true, ".mkv": true, ".mp4": true, ".m4v": true,
	".mov": true, ".ts": true, ".m2ts": true, ".webm": true, ".wma": true,
	".ape": true, ".wv": true, ".aiff": true, ".aif": true, ".caf": true,
	".mxf": true, ".oga": true, ".amr": true, ".dts": true, ".truehd": true,
}

func expandStandaloneTargets(args []string, mode, reference string) ([]string, error) {
	refAbs, _ := filepath.Abs(reference)
	seen := map[string]bool{}
	var out []string
	add := func(path string, scanned bool) {
		ext := strings.ToLower(filepath.Ext(path))
		valid := subtitle.IsTextExtension(path)
		if mode == "audio" {
			valid = standaloneAudioExt[ext]
		}
		// Explicit files are always attempted: FFmpeg supports far more formats
		// than any extension table can reasonably enumerate. Directory scans stay
		// filtered so images/documents are not sent to ffprobe by accident.
		if !scanned {
			valid = true
		}
		if scanned && strings.Contains(strings.ToLower(filepath.Base(path)), ".synced.") {
			return
		}
		abs, _ := filepath.Abs(path)
		if valid && abs != refAbs && !seen[abs] {
			seen[abs] = true
			out = append(out, path)
		}
	}
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, fmt.Errorf("target %s: %w", arg, err)
		}
		if !info.IsDir() {
			add(arg, false)
			continue
		}
		err = filepath.WalkDir(arg, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				add(path, true)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func standaloneOutput(target string, f *standaloneFlags, mode string) string {
	if f.output != "" {
		if f.outDir != "" {
			return filepath.Join(f.outDir, filepath.Base(f.output))
		}
		return f.output
	}
	ext := strings.ToLower(filepath.Ext(target))
	if f.format != "" {
		ext = "." + strings.TrimPrefix(strings.ToLower(f.format), ".")
	} else if mode == "audio" && !isStandaloneAudioContainer(ext) {
		ext = ".mka"
	} else if mode == "subtitles" && !isWritableSubtitleExtension(ext) {
		ext = ".vtt"
	}
	stem := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	// Release tooling often leaves a ".raw" marker on extracted audio or a
	// dangling "-.lang" separator on subtitles. Synced deliverables should be
	// clean without changing the source filenames.
	stem = strings.TrimSuffix(stem, ".raw")
	stem = strings.ReplaceAll(stem, "-.", ".")
	stem = strings.TrimRight(stem, "-_. ")
	base := stem + ".synced" + ext
	dir := f.outDir
	if dir == "" {
		dir = filepath.Dir(target)
	}
	return filepath.Join(dir, base)
}

func isWritableSubtitleExtension(ext string) bool {
	switch ext {
	case ".srt", ".vtt", ".webvtt", ".ass", ".ssa", ".ttml":
		return true
	default:
		return false
	}
}

func isStandaloneAudioContainer(ext string) bool {
	switch ext {
	case ".aac", ".ac3", ".eac3", ".ec3", ".m4a", ".mp3", ".flac", ".wav", ".wave", ".ogg", ".opus", ".mka", ".wma", ".wv", ".aiff", ".aif":
		return true
	default:
		return false
	}
}

func audioMinScore(v float64) float64 {
	if v > 0 {
		return v
	}
	return 4
}

func subtitleMinScore(v float64) float64 {
	if v > 0 {
		return v
	}
	return 0.10
}

func timingDescription(scale float64) string {
	if scale <= 0 || math.Abs(scale-1) < 0.00002 {
		return "none (same timing)"
	}
	type fpsPair struct {
		scale       float64
		target, ref string
	}
	pairs := []fpsPair{
		{25 / (24000.0 / 1001), "25", "23.976"},
		{(24000.0 / 1001) / 25, "23.976", "25"},
		{25.0 / 24, "25", "24"},
		{24.0 / 25, "24", "25"},
		{24 / (24000.0 / 1001), "24", "23.976"},
		{(24000.0 / 1001) / 24, "23.976", "24"},
		{30 / (30000.0 / 1001), "30", "29.970"},
		{(30000.0 / 1001) / 30, "29.970", "30"},
	}
	for _, pair := range pairs {
		if math.Abs(scale-pair.scale) < 0.00015 {
			return pair.target + " → " + pair.ref + " fps timing"
		}
	}
	return fmt.Sprintf("linear drift (%+.0f ppm)", (scale-1)*1_000_000)
}
