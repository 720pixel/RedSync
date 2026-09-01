package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/720pixel/RedSync/internal/subtitle"
	rsync "github.com/720pixel/RedSync/internal/sync"
)

const standaloneEventPrefix = "[redsync-event] "

// standaloneEvent is the stable, stderr-only progress contract for `sync`.
// Paths are intentionally reduced to basenames so a live-log consumer does not
// accidentally expose filesystem layout. Optional metrics are pointers so a
// real zero is distinguishable from a field that does not apply to an event.
type standaloneEvent struct {
	SchemaVersion     int      `json:"schema_version"`
	Event             string   `json:"event"`
	Message           string   `json:"message"`
	Target            string   `json:"target"`
	Current           int      `json:"current"`
	Index             int      `json:"index"`
	Total             int      `json:"total"`
	Mode              string   `json:"mode"`
	ReferenceBasename string   `json:"reference_basename"`
	TargetBasename    string   `json:"target_basename"`
	OutputBasename    string   `json:"output_basename"`
	Automatic         *bool    `json:"automatic,omitempty"`
	DryRun            *bool    `json:"dry_run,omitempty"`
	SyncMS            *int     `json:"sync_ms,omitempty"`
	Scale             *float64 `json:"scale,omitempty"`
	DriftPPM          *float64 `json:"drift_ppm,omitempty"`
	Score             *float64 `json:"score,omitempty"`
	OriginalScore     *float64 `json:"original_score,omitempty"`
	Samples           *int     `json:"samples,omitempty"`
	ResidualMS        *int     `json:"residual_ms,omitempty"`
	Segments          *int     `json:"segments,omitempty"`
	Gaps              *int     `json:"gaps,omitempty"`
	Passed            *bool    `json:"passed,omitempty"`
	DurationDeltaMS   *int     `json:"duration_delta_ms,omitempty"`
	ElapsedMS         *int64   `json:"elapsed_ms,omitempty"`
	OutputBytes       *int64   `json:"output_bytes,omitempty"`
	AI                *bool    `json:"ai,omitempty"`
	Model             string   `json:"model,omitempty"`
}

type standaloneEventEmitter struct {
	enabled             bool
	w                   io.Writer
	mode                string
	reference, target   string
	output              string
	index, totalTargets int
}

func newStandaloneEventEmitter(f *standaloneFlags, mode, reference, target, output string, index, total int) standaloneEventEmitter {
	w := f.eventWriter
	if w == nil {
		w = os.Stderr
	}
	return standaloneEventEmitter{
		enabled: f.eventsJSON, w: w, mode: mode,
		reference: reference, target: target, output: output,
		index: index, totalTargets: total,
	}
}

func (e standaloneEventEmitter) emit(name string, add func(*standaloneEvent)) {
	if !e.enabled {
		return
	}
	event := standaloneEvent{
		SchemaVersion: 1,
		Event:         name, Current: e.index, Index: e.index, Total: e.totalTargets, Mode: e.mode,
		ReferenceBasename: filepath.Base(e.reference),
		TargetBasename:    filepath.Base(e.target),
		OutputBasename:    filepath.Base(e.output),
	}
	event.Target = event.TargetBasename
	if add != nil {
		add(&event)
	}
	event.Message = standaloneEventMessage(name, event)
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	// Progress reporting is advisory: a closed log pipe must not invalidate an
	// otherwise successful media result.
	_, _ = fmt.Fprintf(e.w, "%s%s\n", standaloneEventPrefix, b)
}

func standaloneEventMessage(name string, event standaloneEvent) string {
	progress := fmt.Sprintf("%s (%d/%d)", event.TargetBasename, event.Current, event.Total)
	switch name {
	case "target_started":
		return "Starting " + progress
	case "measuring_started":
		return "Analyzing sync for " + progress
	case "measuring_complete":
		return "Analysis complete for " + progress
	case "semantic_ai_started":
		return fmt.Sprintf("AI fallback started for %s: %s is matching sparse cross-language dialogue anchors (no full subtitle translation)", progress, event.Model)
	case "semantic_ai_complete":
		return "AI semantic anchors matched for " + progress + "; RedSync is now calculating and validating timing deterministically"
	case "rendering_started":
		return "Syncing " + progress
	case "rendering_complete":
		return "Sync complete for " + progress
	case "verification_started":
		return "Verifying " + progress
	case "verification_complete":
		if event.Passed != nil && *event.Passed {
			return "Verification passed for " + progress
		}
		return "Verification failed for " + progress
	case "target_complete":
		return "Completed " + progress
	default:
		return name + " " + progress
	}
}

func boolPtr(v bool) *bool          { return &v }
func intPtr(v int) *int             { return &v }
func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }

func setDriftEventMetrics(e *standaloneEvent, drift rsync.Drift) {
	scale := drift.Factor()
	e.SyncMS = intPtr(drift.DelayMS)
	e.Scale = float64Ptr(scale)
	e.DriftPPM = float64Ptr((scale - 1) * 1_000_000)
	e.Score = float64Ptr(drift.Score)
	e.Samples = intPtr(drift.Samples)
	e.ResidualMS = intPtr(drift.ResidualMS)
	e.Segments = intPtr(len(drift.Segments))
	e.Gaps = intPtr(len(drift.Gaps))
}

func setAlignmentEventMetrics(e *standaloneEvent, alignment subtitle.Alignment) {
	e.SyncMS = intPtr(alignment.OffsetMS)
	e.Scale = float64Ptr(alignment.Scale)
	e.DriftPPM = float64Ptr((alignment.Scale - 1) * 1_000_000)
	e.Score = float64Ptr(alignment.Score)
	e.OriginalScore = float64Ptr(alignment.OriginalScore)
	e.Samples = intPtr(alignment.Samples)
	e.ResidualMS = intPtr(alignment.ResidualMS)
	e.Segments = intPtr(len(alignment.Segments))
	e.Gaps = intPtr(len(alignment.Gaps))
}

func setVerificationEventMetrics(e *standaloneEvent, verification *standaloneVerification) {
	e.Passed = boolPtr(verification.Passed)
	e.SyncMS = intPtr(verification.SyncMS)
	e.Scale = float64Ptr(verification.Scale)
	e.DriftPPM = float64Ptr(verification.DriftPPM)
	e.Score = float64Ptr(verification.Score)
	e.Samples = intPtr(verification.Samples)
	e.ResidualMS = intPtr(verification.ResidualMS)
	e.Gaps = intPtr(len(verification.Gaps))
	e.DurationDeltaMS = intPtr(verification.DurationDeltaMS)
}

func setOutputSize(e *standaloneEvent, output string) {
	if info, err := os.Stat(output); err == nil {
		e.OutputBytes = int64Ptr(info.Size())
	}
}
