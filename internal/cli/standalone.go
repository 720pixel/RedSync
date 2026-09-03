package cli

import (
	"context"
	"fmt"
	"io"
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
	output                string
	outDir                string
	format                string
	codec                 string
	bitrate               string
	channels              int
	sampleRate            int
	language              string
	dryRun                bool
	overwrite             bool
	force                 bool
	verify                bool
	detectGaps            bool
	shift                 int
	factor                float64
	maxOffset             float64
	minScore              float64
	minGap                float64
	maxSegments           int
	referenceTrack        int
	targetTrack           int
	semanticCodexModel    string
	semanticCodexBin      string
	semanticCodexTimeout  time.Duration
	semanticWindow        float64
	alignmentPlan         string
	sourceTimelinePlan    string
	verificationReference string
	writePlan             string
	eventsJSON            bool
	eventWriter           io.Writer
}

type standaloneResult struct {
	SchemaVersion         int                     `json:"schema_version"`
	Mode                  string                  `json:"mode"`
	Reference             string                  `json:"reference"`
	Target                string                  `json:"target"`
	Output                string                  `json:"output"`
	Method                string                  `json:"method,omitempty"`
	Language              string                  `json:"language,omitempty"`
	DryRun                bool                    `json:"dry_run"`
	SyncMS                int                     `json:"sync_ms"`
	Scale                 float64                 `json:"scale"`
	DriftPPM              float64                 `json:"drift_ppm"`
	FPSConversion         string                  `json:"fps_conversion"`
	Score                 float64                 `json:"score"`
	OriginalScore         float64                 `json:"original_score,omitempty"`
	Samples               int                     `json:"samples"`
	ResidualMS            int                     `json:"residual_ms"`
	Segments              []timeline.Segment      `json:"segments"`
	Gaps                  []timeline.Gap          `json:"gaps"`
	Verification          *standaloneVerification `json:"verification,omitempty"`
	VerificationReference string                  `json:"verification_reference,omitempty"`
}

type standaloneVerification struct {
	Passed                   bool           `json:"passed"`
	Policy                   string         `json:"policy,omitempty"`
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
	ToleratedGaps            []timeline.Gap `json:"tolerated_gaps,omitempty"`
	FailureReasons           []string       `json:"failure_reasons,omitempty"`
}

const (
	maxSemanticSubtitleResidualMS      = 1500
	maxSemanticSubtitleDurationDeltaMS = 15000
)

func standaloneSyncCmd() *cobra.Command {
	f := &standaloneFlags{referenceTrack: -1, targetTrack: -1, verify: true, detectGaps: true, maxSegments: 16}
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
	fl.IntVar(&f.maxSegments, "max-segments", 16, "maximum piecewise timeline segments")
	fl.IntVar(&f.referenceTrack, "reference-track", -1, "reference audio stream index (default: first audio stream)")
	fl.IntVar(&f.targetTrack, "target-track", -1, "target audio stream index (default: first audio stream)")
	fl.StringVar(&f.semanticCodexModel, "semantic-codex-model", "", "Codex model for the exceptional no-audio cross-language subtitle matcher")
	fl.StringVar(&f.semanticCodexBin, "semantic-codex-bin", "codex", "Codex CLI executable for --semantic-codex-model")
	fl.DurationVar(&f.semanticCodexTimeout, "semantic-codex-timeout", 45*time.Second, "maximum time for sparse Codex semantic matching")
	fl.Float64Var(&f.semanticWindow, "semantic-window", 0, "semantic candidate window in seconds (default: max of 120 and --max-offset)")
	fl.StringVar(&f.alignmentPlan, "alignment-plan", "", "reuse a verified local timeline plan instead of measuring this target")
	fl.StringVar(&f.sourceTimelinePlan, "source-timeline-plan", "", "use a verified audio timeline from the same source container to solve subtitle gaps")
	fl.StringVar(&f.verificationReference, "verification-reference", "", "verify a planned audio sibling against the rendered anchor instead of the original reference")
	fl.StringVar(&f.writePlan, "write-alignment-plan", "", "write the verified single-target timeline to a local JSON plan")
	fl.BoolVar(&f.eventsJSON, "events-json", false, "emit prefixed one-line JSON progress events on stderr")
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
	if f.semanticWindow < 0 || f.semanticCodexTimeout <= 0 {
		return fmt.Errorf("semantic window cannot be negative and Codex timeout must be greater than zero")
	}
	if f.alignmentPlan != "" && (shiftSet || factorSet || f.semanticCodexModel != "" || f.writePlan != "") {
		return fmt.Errorf("--alignment-plan cannot be combined with manual shift/factor, --semantic-codex-model, or --write-alignment-plan")
	}
	if f.sourceTimelinePlan != "" && (f.alignmentPlan != "" || shiftSet || factorSet || f.verificationReference != "") {
		return fmt.Errorf("--source-timeline-plan cannot be combined with --alignment-plan, manual shift/factor, or --verification-reference")
	}
	if f.verificationReference != "" && f.alignmentPlan == "" {
		return fmt.Errorf("--verification-reference requires --alignment-plan")
	}
	if f.verificationReference != "" && (f.dryRun || !f.verify) {
		return fmt.Errorf("--verification-reference requires output rendering and --verify=true")
	}
	if f.writePlan != "" && (f.dryRun || !f.verify) {
		return fmt.Errorf("--write-alignment-plan requires output rendering and --verify=true")
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
	if f.writePlan != "" && len(targets) != 1 {
		return fmt.Errorf("--write-alignment-plan requires exactly one target")
	}

	if mode == "subtitles" {
		if f.verificationReference != "" {
			return fmt.Errorf("--verification-reference is only valid for audio")
		}
		return syncSubtitleTargets(ctx, reference, targets, f, shiftSet, factorSet)
	}
	if f.sourceTimelinePlan != "" {
		return fmt.Errorf("--source-timeline-plan is only valid for subtitles")
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
	verificationRef := ref
	verificationTrack := refTrack
	verificationReference := ""
	if f.verificationReference != "" {
		verificationRef, err = media.Probe(ctx, f.verificationReference)
		if err != nil {
			return fmt.Errorf("verification reference: %w", err)
		}
		verificationTrack, err = chooseAudioTrack(verificationRef, -1)
		if err != nil {
			return fmt.Errorf("verification reference: %w", err)
		}
		verificationReference = f.verificationReference
	}
	var reusablePlan *alignmentPlan
	if f.alignmentPlan != "" {
		plan, err := readAlignmentPlan(f.alignmentPlan, "audio", reference, ref.Duration)
		if err != nil {
			return err
		}
		reusablePlan = &plan
	}
	var results []standaloneResult
	if len(targets) > 1 {
		ui.Section("audio batch")
		ui.Field("targets", fmt.Sprintf("%d files", len(targets)))
	}
	for targetIndex, targetPath := range targets {
		output := standaloneOutput(targetPath, f, "audio")
		events := newStandaloneEventEmitter(f, "audio", reference, targetPath, output, targetIndex+1, len(targets))
		targetStarted := time.Now()
		events.emit("target_started", func(e *standaloneEvent) {
			e.DryRun = boolPtr(f.dryRun)
		})
		target, err := media.Probe(ctx, targetPath)
		if err != nil {
			return fmt.Errorf("target %s: %w", filepath.Base(targetPath), err)
		}
		targetTrack, err := chooseAudioTrack(target, f.targetTrack)
		if err != nil {
			return fmt.Errorf("target %s: %w", filepath.Base(targetPath), err)
		}
		ui.Section("audio sync")
		ui.Field("reference", filepath.Base(reference))
		ui.Field("target", filepath.Base(targetPath))

		var drift rsync.Drift
		measurementStarted := time.Now()
		events.emit("measuring_started", func(e *standaloneEvent) {
			e.Automatic = boolPtr(!shiftSet && !factorSet && reusablePlan == nil)
		})
		method := "measured"
		if reusablePlan != nil {
			if !sameSourceDuration(reusablePlan.AnchorDurationSeconds, target.Duration) {
				return fmt.Errorf("alignment plan anchor duration %.3fs does not match sibling %s duration %.3fs", reusablePlan.AnchorDurationSeconds, filepath.Base(targetPath), target.Duration)
			}
			ui.Step("applying verified sibling timeline plan")
			drift = reusablePlan.drift()
			method = "plan"
		} else if shiftSet || factorSet {
			drift.DelayMS = f.shift
			drift.Scale = f.factor
			method = "manual"
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
		events.emit("measuring_complete", func(e *standaloneEvent) {
			e.Automatic = boolPtr(!shiftSet && !factorSet && reusablePlan == nil)
			setDriftEventMetrics(e, drift)
			e.ElapsedMS = int64Ptr(time.Since(measurementStarted).Milliseconds())
		})
		ui.Field("sync (ms)", fmt.Sprintf("%+d", drift.DelayMS))
		ui.Field("method", method)
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
			renderStarted := time.Now()
			events.emit("rendering_started", nil)
			if err := rsync.RenderAudio(ctx, target, targetTrack, drift, ref.Duration, output, rsync.AudioRenderOptions{
				Codec: f.codec, BitRate: f.bitrate, Channels: f.channels, SampleRate: f.sampleRate,
				Language: f.language, Overwrite: f.overwrite,
			}); err != nil {
				return err
			}
			events.emit("rendering_complete", func(e *standaloneEvent) {
				e.ElapsedMS = int64Ptr(time.Since(renderStarted).Milliseconds())
				setOutputSize(e, output)
			})
			if f.verify {
				ui.Step("verifying the finished audio")
				verificationStarted := time.Now()
				events.emit("verification_started", nil)
				verification, err = verifyAudioOutput(ctx, verificationRef, verificationTrack, output, f)
				if err != nil {
					return fmt.Errorf("verify %s: %w", filepath.Base(output), err)
				}
				reportVerification(verification)
				events.emit("verification_complete", func(e *standaloneEvent) {
					setVerificationEventMetrics(e, verification)
					e.ElapsedMS = int64Ptr(time.Since(verificationStarted).Milliseconds())
				})
			}
			if f.writePlan != "" {
				exported, err := exportVerifiedAlignmentPlan(f.writePlan, f.overwrite, verification, func() (alignmentPlan, error) {
					return planFromDrift(reference, targetPath, ref.Duration, target.Duration, drift, verification)
				})
				if err != nil {
					return err
				}
				if exported {
					ui.Field("alignment plan", f.writePlan)
				}
			}
		}
		language := f.language
		if language == "" {
			language = targetTrack.Language
		}
		results = append(results, standaloneResult{
			SchemaVersion: 2, Mode: "audio", Reference: reference, Target: targetPath, Output: output, Method: method,
			Language: language, DryRun: f.dryRun,
			SyncMS: drift.DelayMS, Scale: drift.Factor(), DriftPPM: (drift.Factor() - 1) * 1_000_000,
			FPSConversion: timingDescription(drift.Factor()), Score: drift.Score,
			Samples: drift.Samples, ResidualMS: drift.ResidualMS, Segments: nonNilSegments(drift.Segments),
			Gaps: nonNilGaps(drift.Gaps), Verification: verification, VerificationReference: verificationReference,
		})
		events.emit("target_complete", func(e *standaloneEvent) {
			e.DryRun = boolPtr(f.dryRun)
			setDriftEventMetrics(e, drift)
			if verification != nil {
				e.Passed = boolPtr(verification.Passed)
			}
			e.ElapsedMS = int64Ptr(time.Since(targetStarted).Milliseconds())
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
	_, referenceEnd := subtitleCueBounds(refCues)
	var reusablePlan *alignmentPlan
	if f.alignmentPlan != "" {
		plan, err := readAlignmentPlan(f.alignmentPlan, "subtitles", reference, referenceEnd)
		if err != nil {
			return err
		}
		reusablePlan = &plan
	}
	var sourceTimelinePlan *alignmentPlan
	if f.sourceTimelinePlan != "" {
		plan, err := readSourceTimelinePlan(f.sourceTimelinePlan)
		if err != nil {
			return err
		}
		sourceTimelinePlan = &plan
	}
	var semanticMatcher subtitle.SemanticAnchorMatcher
	if f.semanticCodexModel != "" {
		semanticMatcher = newCodexSemanticMatcher(f)
	}
	if len(targets) > 1 {
		ui.Section("subtitle batch")
		ui.Field("targets", fmt.Sprintf("%d files", len(targets)))
	}
	for targetIndex, targetPath := range targets {
		output := standaloneOutput(targetPath, f, "subtitles")
		events := newStandaloneEventEmitter(f, "subtitles", reference, targetPath, output, targetIndex+1, len(targets))
		targetStarted := time.Now()
		events.emit("target_started", func(e *standaloneEvent) {
			e.DryRun = boolPtr(f.dryRun)
		})
		targetCues, err := subtitle.Read(ctx, targetPath)
		if err != nil {
			return fmt.Errorf("target %s: %w", filepath.Base(targetPath), err)
		}
		ui.Section("subtitle sync")
		ui.Field("reference", filepath.Base(reference))
		ui.Field("target", filepath.Base(targetPath))
		var alignment subtitle.Alignment
		var activityCandidate *subtitle.Alignment
		semanticAttempts := 0
		measurementStarted := time.Now()
		events.emit("measuring_started", func(e *standaloneEvent) {
			e.Automatic = boolPtr(!shiftSet && !factorSet && reusablePlan == nil && sourceTimelinePlan == nil)
		})
		if reusablePlan != nil {
			ui.Step("applying verified sibling timeline plan")
			alignment = reusablePlan.subtitleAlignment(len(refCues), len(targetCues))
		} else if sourceTimelinePlan != nil {
			ui.Step("applying verified source audio timeline and fitting subtitle residual")
			if semanticMatcher != nil {
				ui.Step("AI fallback: matching sparse translated dialogue anchors with " + f.semanticCodexModel)
				events.emit("semantic_ai_started", func(e *standaloneEvent) {
					e.AI = boolPtr(true)
					e.Model = f.semanticCodexModel
				})
			}
			alignment, err = subtitleAlignmentFromSourceTimeline(ctx, refCues, targetCues, *sourceTimelinePlan, subtitle.AlignOptions{
				MaxOffsetSeconds: math.Min(f.maxOffset, 30),
				MinScore:         subtitleMinScore(f.minScore),
				MinGapSeconds:    f.minGap,
				MaxSegments:      f.maxSegments,
				DisablePiecewise: true,
			}, semanticMatcher, f.semanticWindow)
			if err != nil {
				return fmt.Errorf("apply source timeline to %s: %w", filepath.Base(targetPath), err)
			}
			if semanticMatcher != nil {
				events.emit("semantic_ai_complete", func(e *standaloneEvent) {
					e.AI = boolPtr(true)
					e.Model = f.semanticCodexModel
					setAlignmentEventMetrics(e, alignment)
				})
			}
		} else if shiftSet || factorSet {
			alignment = subtitle.Alignment{Method: "manual", OffsetMS: f.shift, Scale: f.factor, ReferenceCues: len(refCues), TargetCues: len(targetCues)}
		} else {
			minScore := subtitleMinScore(f.minScore)
			if f.force {
				minScore = 0.01
			}
			alignOpts := subtitle.AlignOptions{
				MaxOffsetSeconds: f.maxOffset,
				MinScore:         minScore,
				MinGapSeconds:    f.minGap,
				MaxSegments:      f.maxSegments,
				DisablePiecewise: !f.detectGaps,
			}
			if semanticMatcher == nil {
				ui.Step("matching language-independent subtitle activity")
				alignment, err = subtitle.Align(refCues, targetCues, alignOpts)
			} else {
				// Cross-language cue activity is deterministic, fast, and often more
				// precise than sparse dialogue anchors. Use it when its distributed
				// evidence is strong; retain the candidate so verification can fall
				// back to it if the semantic result is worse.
				activity, activityErr := subtitle.Align(refCues, targetCues, alignOpts)
				if activityErr == nil {
					activity = subtitle.PreserveCrossLanguageCues(refCues, targetCues, activity)
					activity.Method = "activity-cross-language"
					activityCandidate = &activity
				}
				if activityErr == nil && strongCrossLanguageActivity(activity, minScore) {
					ui.Step("using strong deterministic cross-language cue activity")
					alignment = activity
				} else {
					semanticAttempts++
					alignment, err = alignCodexSemanticCandidate(ctx, refCues, targetCues, alignOpts, semanticMatcher, f, events)
					if err != nil && activityCandidate != nil {
						// An AI transport/schema/evidence failure must not discard a usable
						// deterministic candidate. Rendering verification is the final gate.
						ui.Warn("AI fallback could not produce a candidate; validating deterministic activity")
						alignment, err = *activityCandidate, nil
					}
					if err != nil && activityErr != nil {
						err = fmt.Errorf("deterministic activity: %v; semantic fallback: %w", activityErr, err)
					}
				}
			}
			if err != nil {
				return fmt.Errorf("align %s: %w", filepath.Base(targetPath), err)
			}
		}
		events.emit("measuring_complete", func(e *standaloneEvent) {
			e.Automatic = boolPtr(!shiftSet && !factorSet && reusablePlan == nil && sourceTimelinePlan == nil)
			setAlignmentEventMetrics(e, alignment)
			e.ElapsedMS = int64Ptr(time.Since(measurementStarted).Milliseconds())
		})
		ui.Field("sync (ms)", fmt.Sprintf("%+d", alignment.OffsetMS))
		ui.Field("method", alignment.Method)
		ui.Field("scale", fmt.Sprintf("%.9f", alignment.Scale))
		ui.Field("FPS / timing", timingDescription(alignment.Scale))
		if alignment.Score > 0 {
			ui.Field("match", fmt.Sprintf("%.3f (before %.3f, %dms residual)", alignment.Score, alignment.OriginalScore, alignment.ResidualMS))
		}
		reportGaps(alignment.Gaps)
		ui.Field("output", output)
		var verification *standaloneVerification
		if !f.dryRun {
			renderStarted := time.Now()
			events.emit("rendering_started", nil)
			synced := subtitle.Apply(targetCues, alignment)
			if err := subtitle.Write(ctx, output, synced, f.overwrite); err != nil {
				return err
			}
			events.emit("rendering_complete", func(e *standaloneEvent) {
				e.ElapsedMS = int64Ptr(time.Since(renderStarted).Milliseconds())
				setOutputSize(e, output)
			})
			if f.verify {
				ui.Step("verifying the finished subtitles")
				verificationStarted := time.Now()
				events.emit("verification_started", nil)
				var verificationResidual subtitle.Alignment
				var verificationMatcher subtitle.SemanticAnchorMatcher
				verificationCrossLanguage := false
				if reusablePlan != nil {
					// A verified sibling plan already established the source-to-reference
					// timeline with the English anchor. Re-aligning translated cue activity
					// against English here produces false residuals when translations split
					// or merge cues differently. Verify the deterministic plan render itself:
					// every cue, timestamp and line must survive exactly as transformed.
					verification, err = verifyPlannedSubtitleOutput(ctx, synced, output)
				} else {
					verificationMatcher = semanticMatcher
					if semanticMatcher != nil {
						// Candidate generation and verification must not share the same
						// sparse semantic anchors. A cached sample can miss one late edit
						// twice and incorrectly certify its own output. Verify with the
						// next-best distinctive cue from every time bucket instead.
						verificationMatcher = newCodexSemanticMatcherVariant(f, 1)
					}
					verificationCrossLanguage = verificationMatcher != nil || alignment.Method == "activity-cross-language" || alignment.Method == "source_timeline_plan_cross_language_activity"
					verification, verificationResidual, err = verifySubtitleOutput(ctx, refCues, output, f, verificationMatcher, verificationCrossLanguage)
				}
				if err != nil {
					return fmt.Errorf("verify %s: %w", filepath.Base(output), err)
				}
				// A failed first candidate is an internal recovery signal, not a
				// reason to create a manual-review job. Automatically evaluate the
				// other independent timing source and keep whichever result verifies
				// best. The retry overwrites only the output created by this command.
				canRecover := semanticMatcher != nil && reusablePlan == nil && sourceTimelinePlan == nil && !shiftSet && !factorSet
				if !verification.Passed && canRecover {
					originalAlignment, originalSynced, originalVerification := alignment, synced, verification
					var recoveryAlignment subtitle.Alignment
					var recoveryMatcher subtitle.SemanticAnchorMatcher
					var recoveryErr error
					recoveryKind := ""
					switch {
					case alignment.Method == "activity-cross-language" && semanticAttempts == 0:
						recoveryKind = "semantic AI"
						events.emit("automatic_recovery_started", func(e *standaloneEvent) {
							e.AI = boolPtr(true)
							e.Model = f.semanticCodexModel
						})
						semanticAttempts++
						recoveryAlignment, recoveryErr = alignCodexSemanticCandidate(ctx, refCues, targetCues, subtitleAlignOptions(f), semanticMatcher, f, events)
						recoveryMatcher = newCodexSemanticMatcherVariant(f, 1)
					case alignment.Method == "semantic-codex" && activityCandidate != nil:
						recoveryKind = "deterministic activity"
						events.emit("automatic_recovery_started", func(e *standaloneEvent) {
							e.AI = boolPtr(false)
						})
						recoveryAlignment = *activityCandidate
						recoveryMatcher = newCodexSemanticMatcherVariant(f, semanticAttempts+1)
					}
					if recoveryKind != "" && recoveryErr == nil {
						ui.Step("automatic recovery: validating " + recoveryKind + " candidate")
						recoverySynced := subtitle.Apply(targetCues, recoveryAlignment)
						if err := subtitle.Write(ctx, output, recoverySynced, true); err != nil {
							return fmt.Errorf("render automatic recovery for %s: %w", filepath.Base(output), err)
						}
						recoveryVerification, recoveryResidual, verifyErr := verifySubtitleOutput(ctx, refCues, output, f, recoveryMatcher, true)
						if verifyErr != nil {
							recoveryErr = verifyErr
							if restoreErr := subtitle.Write(ctx, output, originalSynced, true); restoreErr != nil {
								return fmt.Errorf("restore subtitle candidate after failed recovery verification for %s: %w", filepath.Base(output), restoreErr)
							}
							alignment, synced, verification = originalAlignment, originalSynced, originalVerification
						} else if betterSubtitleVerification(recoveryVerification, originalVerification) {
							alignment, synced, verification = recoveryAlignment, recoverySynced, recoveryVerification
							verificationResidual, verificationMatcher, verificationCrossLanguage = recoveryResidual, recoveryMatcher, true
						} else if err := subtitle.Write(ctx, output, originalSynced, true); err != nil {
							return fmt.Errorf("restore verified subtitle candidate for %s: %w", filepath.Base(output), err)
						} else {
							alignment, synced, verification = originalAlignment, originalSynced, originalVerification
						}
					}
					if recoveryKind != "" {
						events.emit("automatic_recovery_complete", func(e *standaloneEvent) {
							e.AI = boolPtr(recoveryKind == "semantic AI")
							e.Model = f.semanticCodexModel
							if verification != nil {
								setVerificationEventMetrics(e, verification)
							}
						})
					}
					if recoveryErr != nil {
						ui.Warn("automatic " + recoveryKind + " recovery could not be validated: " + recoveryErr.Error())
					}
				}
				// Semantic anchor selection can vary between otherwise valid sparse
				// matches. Reject a bad sample from its deterministic verification
				// metrics and retry with a fresh matcher inside this command, rather
				// than failing the whole batch and relying on an operator to requeue it.
				for !verification.Passed && canRecover && semanticAttempts < 3 {
					semanticAttempts++
					ui.Step(fmt.Sprintf("automatic recovery: retrying AI semantic anchors (%d/3)", semanticAttempts))
					events.emit("semantic_ai_retry_started", func(e *standaloneEvent) {
						e.AI = boolPtr(true)
						e.Model = f.semanticCodexModel
					})
					retryMatcher := newCodexSemanticMatcherVariant(f, semanticAttempts)
					retryAlignment, retryErr := alignCodexSemanticCandidate(ctx, refCues, targetCues, subtitleAlignOptions(f), retryMatcher, f, events)
					if retryErr != nil {
						ui.Warn("automatic AI retry could not produce timing evidence: " + retryErr.Error())
						continue
					}
					retrySynced := subtitle.Apply(targetCues, retryAlignment)
					if err := subtitle.Write(ctx, output, retrySynced, true); err != nil {
						return fmt.Errorf("render automatic AI retry for %s: %w", filepath.Base(output), err)
					}
					retryVerificationMatcher := newCodexSemanticMatcherVariant(f, semanticAttempts+1)
					retryVerification, retryResidual, verifyErr := verifySubtitleOutput(ctx, refCues, output, f, retryVerificationMatcher, true)
					if verifyErr != nil {
						if restoreErr := subtitle.Write(ctx, output, synced, true); restoreErr != nil {
							return fmt.Errorf("restore subtitle after failed AI retry for %s: %w", filepath.Base(output), restoreErr)
						}
						ui.Warn("automatic AI retry could not be verified: " + verifyErr.Error())
						continue
					}
					if betterSubtitleVerification(retryVerification, verification) {
						alignment, synced, verification, verificationResidual = retryAlignment, retrySynced, retryVerification, retryResidual
						verificationMatcher, verificationCrossLanguage = retryVerificationMatcher, true
					} else if err := subtitle.Write(ctx, output, synced, true); err != nil {
						return fmt.Errorf("restore subtitle after rejected AI retry for %s: %w", filepath.Base(output), err)
					}
					events.emit("semantic_ai_retry_complete", func(e *standaloneEvent) {
						e.AI = boolPtr(true)
						e.Model = f.semanticCodexModel
						setVerificationEventMetrics(e, verification)
					})
				}
				// A sparse semantic fit can leave a tiny global clock residual after
				// piecewise cue-boundary rendering. Correct that measurement once and
				// verify the final composition instead of sending a sub-frame mismatch
				// to manual review. Material gaps remain governed by the bounded
				// cross-language gap policy above.
				canCorrectResidual := verification != nil && !verification.Passed && verificationMatcher != nil &&
					len(verificationResidual.Gaps) == 0 && absInt(verificationResidual.OffsetMS) <= 500 &&
					math.Abs((verificationResidual.Scale-1)*1_000_000) <= 500 && verificationResidual.ResidualMS <= maxSemanticSubtitleResidualMS
				if canCorrectResidual {
					ui.Step("automatic recovery: applying the verified residual clock correction")
					events.emit("automatic_residual_correction_started", func(e *standaloneEvent) {
						setAlignmentEventMetrics(e, verificationResidual)
					})
					correctedAlignment := composeGlobalSubtitleResidual(alignment, verificationResidual)
					correctedSynced := subtitle.Apply(targetCues, correctedAlignment)
					if err := subtitle.Write(ctx, output, correctedSynced, true); err != nil {
						return fmt.Errorf("render automatic residual correction for %s: %w", filepath.Base(output), err)
					}
					// The matcher that measured the residual is now part of candidate
					// generation evidence. Certify the correction with another fresh cue
					// variant; otherwise its own affine correction will trivially produce
					// zero offset and drift on the same sparse pairs.
					correctedVerificationMatcher := newCodexSemanticMatcherVariant(f, semanticAttempts+2)
					correctedVerification, correctedResidual, verifyErr := verifySubtitleOutput(ctx, refCues, output, f, correctedVerificationMatcher, verificationCrossLanguage)
					if verifyErr != nil {
						if restoreErr := subtitle.Write(ctx, output, synced, true); restoreErr != nil {
							return fmt.Errorf("restore subtitle after residual correction for %s: %w", filepath.Base(output), restoreErr)
						}
						ui.Warn("automatic residual correction could not be verified: " + verifyErr.Error())
					} else if betterSubtitleVerification(correctedVerification, verification) {
						alignment, synced, verification, verificationResidual = correctedAlignment, correctedSynced, correctedVerification, correctedResidual
						verificationMatcher = correctedVerificationMatcher
					} else if err := subtitle.Write(ctx, output, synced, true); err != nil {
						return fmt.Errorf("restore subtitle after rejected residual correction for %s: %w", filepath.Base(output), err)
					}
					events.emit("automatic_residual_correction_complete", func(e *standaloneEvent) {
						setVerificationEventMetrics(e, verification)
					})
				}
				reportVerification(verification)
				events.emit("verification_complete", func(e *standaloneEvent) {
					setVerificationEventMetrics(e, verification)
					e.ElapsedMS = int64Ptr(time.Since(verificationStarted).Milliseconds())
				})
			}
			if f.writePlan != "" {
				_, targetEnd := subtitleCueBounds(targetCues)
				exported, err := exportVerifiedAlignmentPlan(f.writePlan, f.overwrite, verification, func() (alignmentPlan, error) {
					return planFromSubtitle(reference, targetPath, referenceEnd, targetEnd, alignment, verification)
				})
				if err != nil {
					return err
				}
				if exported {
					ui.Field("alignment plan", f.writePlan)
				}
			}
		}
		results = append(results, standaloneResult{
			SchemaVersion: 2, Mode: "subtitles", Reference: reference, Target: targetPath, Output: output, Method: alignment.Method, DryRun: f.dryRun,
			SyncMS: alignment.OffsetMS, Scale: alignment.Scale, DriftPPM: (alignment.Scale - 1) * 1_000_000,
			FPSConversion: timingDescription(alignment.Scale), Score: alignment.Score, OriginalScore: alignment.OriginalScore,
			Samples: alignment.Samples, ResidualMS: alignment.ResidualMS, Segments: nonNilSegments(alignment.Segments),
			Gaps: nonNilGaps(alignment.Gaps), Verification: verification,
		})
		events.emit("target_complete", func(e *standaloneEvent) {
			e.DryRun = boolPtr(f.dryRun)
			setAlignmentEventMetrics(e, alignment)
			if verification != nil {
				e.Passed = boolPtr(verification.Passed)
			}
			e.ElapsedMS = int64Ptr(time.Since(targetStarted).Milliseconds())
		})
	}
	if flagJSON {
		return emitJSON(results)
	}
	return nil
}

func subtitleAlignOptions(f *standaloneFlags) subtitle.AlignOptions {
	minScore := subtitleMinScore(f.minScore)
	if f.force {
		minScore = 0.01
	}
	return subtitle.AlignOptions{
		MaxOffsetSeconds: f.maxOffset,
		MinScore:         minScore,
		MinGapSeconds:    f.minGap,
		MaxSegments:      f.maxSegments,
		DisablePiecewise: !f.detectGaps,
	}
}

func newCodexSemanticMatcher(f *standaloneFlags) subtitle.SemanticAnchorMatcher {
	return newCodexSemanticMatcherVariant(f, 0)
}

func newCodexSemanticMatcherVariant(f *standaloneFlags, variant int) subtitle.SemanticAnchorMatcher {
	return &subtitle.CodexAnchorMatcher{
		Binary: f.semanticCodexBin, Model: f.semanticCodexModel,
		ReasoningEffort: "low", Timeout: f.semanticCodexTimeout,
		MaxAnchors: 40, CueSelectionVariant: variant,
	}
}

func strongCrossLanguageActivity(alignment subtitle.Alignment, minScore float64) bool {
	return alignment.Score >= math.Max(0.70, minScore) &&
		alignment.Samples >= 8 && alignment.ResidualMS <= 350 &&
		alignment.Scale >= 0.8 && alignment.Scale <= 1.2
}

func alignCodexSemanticCandidate(ctx context.Context, reference, target []subtitle.Cue, alignOpts subtitle.AlignOptions, matcher subtitle.SemanticAnchorMatcher, f *standaloneFlags, events standaloneEventEmitter) (subtitle.Alignment, error) {
	ui.Step("AI fallback: matching sparse translated dialogue anchors with " + f.semanticCodexModel)
	events.emit("semantic_ai_started", func(e *standaloneEvent) {
		e.AI = boolPtr(true)
		e.Model = f.semanticCodexModel
	})
	alignment, err := subtitle.AlignCodexSemantic(ctx, reference, target, matcher, subtitle.SemanticOptions{
		AlignOptions: alignOpts, SearchWindowSeconds: f.semanticWindow,
	})
	if err != nil {
		return subtitle.Alignment{}, err
	}
	events.emit("semantic_ai_complete", func(e *standaloneEvent) {
		e.AI = boolPtr(true)
		e.Model = f.semanticCodexModel
		setAlignmentEventMetrics(e, alignment)
	})
	return alignment, nil
}

func betterSubtitleVerification(candidate, current *standaloneVerification) bool {
	if candidate == nil {
		return false
	}
	if current == nil || candidate.Passed != current.Passed {
		return current == nil || candidate.Passed
	}
	if len(candidate.FailureReasons) != len(current.FailureReasons) {
		return len(candidate.FailureReasons) < len(current.FailureReasons)
	}
	penalty := func(v *standaloneVerification) float64 {
		return math.Abs(float64(v.SyncMS)) + math.Abs(v.DriftPPM)*2 + float64(v.ResidualMS)*2 + float64(len(v.Gaps))*10_000
	}
	return penalty(candidate) < penalty(current)
}

// composeGlobalSubtitleResidual combines a target-to-reference alignment with
// one residual affine correction measured on its rendered output. The residual
// is deliberately required by the caller to contain no piecewise gaps.
func composeGlobalSubtitleResidual(base, residual subtitle.Alignment) subtitle.Alignment {
	scale := residual.Scale
	if scale <= 0 {
		scale = 1
	}
	mapResidualMS := func(ms int) int {
		return int(math.Round(float64(ms)*scale)) + residual.OffsetMS
	}
	out := base
	out.Segments = append([]timeline.Segment(nil), base.Segments...)
	out.Gaps = append([]timeline.Gap(nil), base.Gaps...)
	out.Method += "+residual"
	out.Scale *= scale
	out.OffsetMS = mapResidualMS(out.OffsetMS)
	for i := range out.Segments {
		out.Segments[i].Scale *= scale
		out.Segments[i].OffsetMS = mapResidualMS(out.Segments[i].OffsetMS)
		out.Segments[i].ReferenceStartMS = mapResidualMS(out.Segments[i].ReferenceStartMS)
		out.Segments[i].ReferenceEndMS = mapResidualMS(out.Segments[i].ReferenceEndMS)
	}
	for i := range out.Gaps {
		wasTargetOnly := out.Gaps[i].DeltaMS < 0
		oldDurationMS := out.Gaps[i].DurationMS
		out.Gaps[i].ReferenceBeforeMS = mapResidualMS(out.Gaps[i].ReferenceBeforeMS)
		out.Gaps[i].ReferenceAfterMS = mapResidualMS(out.Gaps[i].ReferenceAfterMS)
		if wasTargetOnly {
			out.Gaps[i].DurationMS = int(math.Round(float64(oldDurationMS) * scale))
			out.Gaps[i].DeltaMS = -out.Gaps[i].DurationMS
			out.Gaps[i].Action = "remove_target"
			continue
		}
		out.Gaps[i].DeltaMS = out.Gaps[i].ReferenceAfterMS - out.Gaps[i].ReferenceBeforeMS
		out.Gaps[i].DurationMS = absInt(out.Gaps[i].DeltaMS)
		out.Gaps[i].Action = "insert_silence"
	}
	return out
}

// exportVerifiedAlignmentPlan keeps plan persistence downstream of the
// verification gate without replacing a useful failed-verification result with
// a secondary export error. Callers can still emit their complete JSON result,
// and automation gets the actual offset/drift/residual diagnostics.
func exportVerifiedAlignmentPlan(path string, overwrite bool, verification *standaloneVerification, build func() (alignmentPlan, error)) (bool, error) {
	if verification == nil || !verification.Passed {
		ui.Warn("alignment plan was not exported because verification did not pass")
		return false, nil
	}
	plan, err := build()
	if err != nil {
		return false, err
	}
	if err := writeAlignmentPlan(path, plan, overwrite); err != nil {
		return false, err
	}
	return true, nil
}

func subtitleAlignmentFromSourceTimeline(ctx context.Context, reference, target []subtitle.Cue, plan alignmentPlan, opts subtitle.AlignOptions, semanticMatcher subtitle.SemanticAnchorMatcher, semanticWindow float64) (subtitle.Alignment, error) {
	if len(reference) < 3 || len(target) < 3 {
		return subtitle.Alignment{}, fmt.Errorf("need at least 3 cues in both reference and target")
	}
	_, targetEnd := subtitleCueBounds(target)
	coverageEnd := float64(plan.Segments[len(plan.Segments)-1].TargetEndMS) / 1000
	if targetEnd > coverageEnd+2 {
		return subtitle.Alignment{}, fmt.Errorf("subtitle ends at %.3fs beyond source timeline coverage %.3fs", targetEnd, coverageEnd)
	}

	base := plan.subtitleAlignment(len(reference), len(target))
	provisional := subtitle.Apply(target, base)
	activityResidual, activityErr := subtitle.Align(reference, provisional, opts)
	residual, err := activityResidual, activityErr
	method := "source_timeline_plan"
	if semanticMatcher != nil {
		if semanticWindow <= 0 || semanticWindow > 30 {
			semanticWindow = 30
		}
		semanticResidual, semanticErr := subtitle.AlignCodexSemantic(ctx, reference, provisional, semanticMatcher, subtitle.SemanticOptions{
			AlignOptions: opts, SearchWindowSeconds: semanticWindow,
		})
		semanticSafe := semanticErr == nil && len(semanticResidual.Gaps) == 0 && math.Abs((semanticResidual.Scale-1)*1_000_000) <= 50 && semanticResidual.ResidualMS <= 350 && absInt(semanticResidual.OffsetMS) <= 30_000
		activitySafe := activityErr == nil && len(activityResidual.Gaps) == 0 && math.Abs((activityResidual.Scale-1)*1_000_000) <= 50 && activityResidual.ResidualMS <= 250 && absInt(activityResidual.OffsetMS) <= 30_000
		switch {
		case semanticSafe && (!activitySafe || semanticResidual.ResidualMS <= activityResidual.ResidualMS):
			residual, err = semanticResidual, nil
			method = "source_timeline_plan_semantic"
		case activitySafe:
			residual, err = activityResidual, nil
			method = "source_timeline_plan_cross_language_activity"
		case semanticErr != nil:
			return subtitle.Alignment{}, fmt.Errorf("fit semantic subtitle residual: %w; deterministic activity fallback: %v", semanticErr, activityErr)
		default:
			residual, err = semanticResidual, nil
			method = "source_timeline_plan_semantic"
		}
	}
	if err != nil {
		return subtitle.Alignment{}, fmt.Errorf("fit subtitle residual: %w", err)
	}
	ppm := (residual.Scale - 1) * 1_000_000
	maxResidualMS := 100
	if semanticMatcher != nil {
		// Translations legitimately split and merge dialogue at slightly
		// different points. The verified audio plan already fixes the edit
		// topology, so semantic evidence only fits one bounded residual.
		maxResidualMS = 350
	}
	if len(residual.Gaps) != 0 || math.Abs(ppm) > 50 || residual.ResidualMS > maxResidualMS || absInt(residual.OffsetMS) > 30_000 {
		return subtitle.Alignment{}, fmt.Errorf("source timeline left unsafe subtitle residual (%+dms, %+.0f ppm, %dms residual, %d gaps)", residual.OffsetMS, ppm, residual.ResidualMS, len(residual.Gaps))
	}

	base.Method = method
	base.OffsetMS += residual.OffsetMS
	base.Score = residual.Score
	base.OriginalScore = residual.OriginalScore
	base.Samples = residual.Samples
	base.ResidualMS = residual.ResidualMS
	for i := range base.Segments {
		base.Segments[i].OffsetMS += residual.OffsetMS
		base.Segments[i].ReferenceStartMS += residual.OffsetMS
		base.Segments[i].ReferenceEndMS += residual.OffsetMS
	}
	for i := range base.Gaps {
		base.Gaps[i].ReferenceBeforeMS += residual.OffsetMS
		base.Gaps[i].ReferenceAfterMS += residual.OffsetMS
	}
	return base, nil
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

func verifySubtitleOutput(ctx context.Context, reference []subtitle.Cue, output string, f *standaloneFlags, semanticMatcher subtitle.SemanticAnchorMatcher, crossLanguage bool) (*standaloneVerification, subtitle.Alignment, error) {
	finished, err := subtitle.Read(ctx, output)
	if err != nil {
		return nil, subtitle.Alignment{}, err
	}
	alignOpts := subtitle.AlignOptions{
		MaxOffsetSeconds: math.Min(f.maxOffset, 30), MinScore: subtitleMinScore(f.minScore),
		MinGapSeconds: f.minGap, MaxSegments: f.maxSegments,
	}
	var a subtitle.Alignment
	if semanticMatcher != nil {
		window := f.semanticWindow
		if window <= 0 || window > 30 {
			window = 30
		}
		a, err = subtitle.AlignCodexSemantic(ctx, reference, finished, semanticMatcher, subtitle.SemanticOptions{
			AlignOptions: alignOpts, SearchWindowSeconds: window,
		})
	} else {
		a, err = subtitle.Align(reference, finished, alignOpts)
	}
	if err != nil {
		return nil, subtitle.Alignment{}, err
	}
	ppm := (a.Scale - 1) * 1_000_000
	_, referenceEnd := subtitleCueBounds(reference)
	_, outputEnd := subtitleCueBounds(finished)
	maxResidualMS := 100
	maxDurationDeltaMS := 0
	policy := "strict"
	remainingGaps := nonNilGaps(a.Gaps)
	var toleratedGaps []timeline.Gap
	maxOffsetMS, maxDriftPPM := 80, 50.0
	if crossLanguage {
		// Cross-language subtitle authors split, merge, and edge cues
		// differently. A small residual clock correction or one/two sub-frame
		// discontinuities are cue jitter, not evidence that the rendered
		// programme is wrong.
		policy = "cross-language"
		maxOffsetMS, maxDriftPPM, maxResidualMS = 120, 250, 500
		if semanticMatcher != nil {
			policy = "cross-language-independent"
			referenceEnd = subtitleProgrammeCueEnd(reference)
			outputEnd = subtitleProgrammeCueEnd(finished)
			// A fresh alternate semantic sample independently verifies the rendered
			// timeline. Translation split/merge jitter can still broaden its median
			// residual, but material offset, drift, and edit topology remain bounded.
			maxResidualMS = maxSemanticSubtitleResidualMS
			// A large unmatched tail is unsafe even when every semantic anchor
			// agrees. This specifically prevents target-only credits or a missed
			// late programme edit from being invisible to sparse cue matching.
			maxDurationDeltaMS = maxSemanticSubtitleDurationDeltaMS
		}
		remainingGaps, toleratedGaps = crossLanguageVerificationGaps(a.Gaps, semanticMatcher != nil)
	} else if f.sourceTimelinePlan != "" && f.semanticCodexModel != "" {
		maxResidualMS = 250
	}
	durationDelta := int(math.Round((outputEnd - referenceEnd) * 1000))
	passed := absInt(a.OffsetMS) <= maxOffsetMS && math.Abs(ppm) <= maxDriftPPM && a.ResidualMS <= maxResidualMS && len(remainingGaps) == 0 && (maxDurationDeltaMS == 0 || absInt(durationDelta) <= maxDurationDeltaMS)
	return &standaloneVerification{
		Passed: passed, Policy: policy,
		SyncMS: a.OffsetMS, Scale: a.Scale, DriftPPM: ppm,
		FPSConversion: timingDescription(a.Scale), Score: a.Score,
		Samples: a.Samples, ResidualMS: a.ResidualMS,
		ReferenceDurationSeconds: referenceEnd, OutputDurationSeconds: outputEnd, DurationDeltaMS: durationDelta,
		Gaps: remainingGaps, ToleratedGaps: toleratedGaps,
		FailureReasons: subtitleVerificationFailures(a.OffsetMS, ppm, a.ResidualMS, durationDelta, maxOffsetMS, maxDriftPPM, maxResidualMS, maxDurationDeltaMS, remainingGaps),
	}, a, nil
}

func crossLanguageVerificationGaps(gaps []timeline.Gap, semantic bool) (remaining, tolerated []timeline.Gap) {
	if len(gaps) == 0 {
		return []timeline.Gap{}, nil
	}
	if len(gaps) > 2 {
		return nonNilGaps(gaps), nil
	}
	maxGapMS, maxTotalMS := 500, 750
	if semantic {
		// The sparse semantic fit safely permits up to 1500ms median cue
		// split/merge variation. A residual one/two-boundary step inside the
		// same envelope is not independent evidence of a programme edit when
		// the rendered offset and clock rate also verify.
		maxGapMS, maxTotalMS = 1000, maxSemanticSubtitleResidualMS
	}
	totalMS := 0
	for _, gap := range gaps {
		if gap.DurationMS > maxGapMS {
			return nonNilGaps(gaps), nil
		}
		totalMS += gap.DurationMS
	}
	if totalMS > maxTotalMS {
		return nonNilGaps(gaps), nil
	}
	return []timeline.Gap{}, nonNilGaps(gaps)
}

func subtitleVerificationFailures(offsetMS int, driftPPM float64, residualMS, durationDeltaMS, maxOffsetMS int, maxDriftPPM float64, maxResidualMS, maxDurationDeltaMS int, gaps []timeline.Gap) []string {
	var failures []string
	if absInt(offsetMS) > maxOffsetMS {
		failures = append(failures, fmt.Sprintf("remaining offset %dms exceeds %dms", offsetMS, maxOffsetMS))
	}
	if math.Abs(driftPPM) > maxDriftPPM {
		failures = append(failures, fmt.Sprintf("remaining drift %+.0fppm exceeds %.0fppm", driftPPM, maxDriftPPM))
	}
	if residualMS > maxResidualMS {
		failures = append(failures, fmt.Sprintf("residual %dms exceeds %dms", residualMS, maxResidualMS))
	}
	if maxDurationDeltaMS > 0 && absInt(durationDeltaMS) > maxDurationDeltaMS {
		failures = append(failures, fmt.Sprintf("cue-end duration delta %+dms exceeds %dms", durationDeltaMS, maxDurationDeltaMS))
	}
	if len(gaps) > 0 {
		failures = append(failures, fmt.Sprintf("%d material discontinuity edit(s) remain", len(gaps)))
	}
	return failures
}

func verifyPlannedSubtitleOutput(ctx context.Context, expected []subtitle.Cue, output string) (*standaloneVerification, error) {
	finished, err := subtitle.Read(ctx, output)
	if err != nil {
		return nil, err
	}
	_, expectedEnd := subtitleCueBounds(expected)
	_, outputEnd := subtitleCueBounds(finished)
	durationDelta := int(math.Round((outputEnd - expectedEnd) * 1000))
	matching := len(expected) > 0 && len(expected) == len(finished)
	maxDeltaMS := 0
	syncMS := 0
	if len(expected) > 0 && len(finished) > 0 {
		syncMS = int(math.Round(float64(finished[0].Start-expected[0].Start) / float64(time.Millisecond)))
	}
	if matching {
		for i := range expected {
			startDelta := absInt(int(math.Round(float64(finished[i].Start-expected[i].Start) / float64(time.Millisecond))))
			endDelta := absInt(int(math.Round(float64(finished[i].End-expected[i].End) / float64(time.Millisecond))))
			maxDeltaMS = max(maxDeltaMS, startDelta, endDelta)
			if strings.Join(finished[i].Text, "\n") != strings.Join(expected[i].Text, "\n") {
				matching = false
				break
			}
		}
	}
	passed := matching && maxDeltaMS <= 2
	score := 0.0
	if passed {
		score = 1
	}
	return &standaloneVerification{
		Passed: passed, Policy: "plan-integrity", SyncMS: syncMS, Scale: 1, DriftPPM: 0,
		FPSConversion: timingDescription(1), Score: score,
		Samples: min(len(expected), len(finished)), ResidualMS: maxDeltaMS,
		ReferenceDurationSeconds: expectedEnd, OutputDurationSeconds: outputEnd, DurationDeltaMS: durationDelta,
		Gaps: []timeline.Gap{},
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

// subtitleProgrammeCueEnd excludes a tiny isolated credit block from the
// duration sanity check. Translation credits are commonly placed about a
// minute after the final dialogue while the English reference's captioning
// credit is adjacent to it; treating those metadata cues as programme timing
// creates a false one-minute mismatch. The cues remain in the rendered file.
func subtitleProgrammeCueEnd(cues []subtitle.Cue) float64 {
	_, last := subtitleCueBounds(cues)
	if len(cues) < 2 {
		return last
	}
	for tailStart := len(cues) - 1; tailStart > 0; tailStart-- {
		gap := cues[tailStart].Start - cues[tailStart-1].End
		if gap <= 30*time.Second {
			continue
		}
		tailSpan := cues[len(cues)-1].End - cues[tailStart].Start
		if len(cues)-tailStart <= 3 && tailSpan <= 30*time.Second {
			return float64(cues[tailStart-1].End) / float64(time.Second)
		}
		break
	}
	return last
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
