package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/720pixel/RedSync/internal/subtitle"
	rsync "github.com/720pixel/RedSync/internal/sync"
	"github.com/720pixel/RedSync/internal/timeline"
)

const alignmentPlanSchemaVersion = 1

// alignmentPlan is a verified target-to-reference timeline that can be reused
// by sibling tracks from the same source release. It contains no absolute
// paths. A reference digest prevents accidentally applying it against another
// episode or reference cut.
type alignmentPlan struct {
	SchemaVersion            int                `json:"schema_version"`
	Mode                     string             `json:"mode"`
	Verified                 bool               `json:"verified"`
	ReferenceBasename        string             `json:"reference_basename"`
	ReferenceSHA256          string             `json:"reference_sha256"`
	ReferenceDurationSeconds float64            `json:"reference_duration_seconds"`
	AnchorBasename           string             `json:"anchor_basename"`
	AnchorDurationSeconds    float64            `json:"anchor_duration_seconds,omitempty"`
	SyncMS                   int                `json:"sync_ms"`
	Scale                    float64            `json:"scale"`
	Score                    float64            `json:"score"`
	Samples                  int                `json:"samples"`
	ResidualMS               int                `json:"residual_ms"`
	Segments                 []timeline.Segment `json:"segments"`
	Gaps                     []timeline.Gap     `json:"gaps"`
	Verification             verificationGate   `json:"verification"`
}

type verificationGate struct {
	Passed     bool    `json:"passed"`
	SyncMS     int     `json:"sync_ms"`
	Scale      float64 `json:"scale"`
	DriftPPM   float64 `json:"drift_ppm"`
	ResidualMS int     `json:"residual_ms"`
}

func readAlignmentPlan(path, mode, referencePath string, referenceDuration float64) (alignmentPlan, error) {
	plan, err := decodeAlignmentPlan(path)
	if err != nil {
		return alignmentPlan{}, err
	}
	if err := validateAlignmentPlan(plan, mode); err != nil {
		return alignmentPlan{}, err
	}
	if !sameDuration(plan.ReferenceDurationSeconds, referenceDuration) {
		return alignmentPlan{}, fmt.Errorf("alignment plan reference duration %.3fs does not match current reference %.3fs", plan.ReferenceDurationSeconds, referenceDuration)
	}
	digest, err := fileSHA256(referencePath)
	if err != nil {
		return alignmentPlan{}, fmt.Errorf("hash alignment-plan reference: %w", err)
	}
	if !strings.EqualFold(plan.ReferenceSHA256, digest) {
		return alignmentPlan{}, fmt.Errorf("alignment plan reference SHA-256 does not match %s", filepath.Base(referencePath))
	}
	return plan, nil
}

func decodeAlignmentPlan(path string) (alignmentPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return alignmentPlan{}, fmt.Errorf("read alignment plan: %w", err)
	}
	var plan alignmentPlan
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return alignmentPlan{}, fmt.Errorf("decode alignment plan: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return alignmentPlan{}, fmt.Errorf("decode alignment plan: multiple JSON values are not allowed")
		}
		return alignmentPlan{}, fmt.Errorf("decode alignment plan trailing data: %w", err)
	}
	return plan, nil
}

// readSourceTimelinePlan loads a verified audio plan for use as timing evidence
// when an English subtitle anchor comes from the same source container. The
// plan stays bound to its original audio reference; the subtitle path performs
// a fresh residual fit and full verification against its own exact reference.
func readSourceTimelinePlan(path string) (alignmentPlan, error) {
	plan, err := decodeAlignmentPlan(path)
	if err != nil {
		return alignmentPlan{}, err
	}
	if err := validateAlignmentPlan(plan, "audio"); err != nil {
		return alignmentPlan{}, fmt.Errorf("source timeline plan: %w", err)
	}
	if len(plan.Segments) == 0 {
		return alignmentPlan{}, fmt.Errorf("source timeline plan has no piecewise timeline")
	}
	return plan, nil
}

func validateAlignmentPlan(plan alignmentPlan, mode string) error {
	if plan.SchemaVersion != alignmentPlanSchemaVersion {
		return fmt.Errorf("unsupported alignment plan schema_version %d (want %d)", plan.SchemaVersion, alignmentPlanSchemaVersion)
	}
	if plan.Mode != mode || (mode != "audio" && mode != "subtitles") {
		return fmt.Errorf("alignment plan mode %q cannot be used for %s", plan.Mode, mode)
	}
	if !plan.Verified || !plan.Verification.Passed {
		return fmt.Errorf("alignment plan is not marked as successfully verified")
	}
	if !finiteSafeScale(plan.Scale) {
		return fmt.Errorf("alignment plan scale %.9f is outside the safe 0.8-1.2 range", plan.Scale)
	}
	if plan.ReferenceDurationSeconds <= 0 || math.IsNaN(plan.ReferenceDurationSeconds) || math.IsInf(plan.ReferenceDurationSeconds, 0) {
		return fmt.Errorf("alignment plan has invalid reference duration")
	}
	if len(plan.ReferenceSHA256) != sha256.Size*2 {
		return fmt.Errorf("alignment plan has invalid reference SHA-256")
	}
	if _, err := hex.DecodeString(plan.ReferenceSHA256); err != nil {
		return fmt.Errorf("alignment plan has invalid reference SHA-256: %w", err)
	}
	if len(plan.Segments) == 0 {
		if len(plan.Gaps) != 0 {
			return fmt.Errorf("alignment plan has gaps without timeline segments")
		}
		return nil
	}
	if len(plan.Segments) > 32 || len(plan.Gaps) != len(plan.Segments)-1 {
		return fmt.Errorf("alignment plan must have 1-32 segments and exactly one gap between adjacent segments")
	}
	if absInt(plan.Segments[0].OffsetMS-plan.SyncMS) > 2 || math.Abs(plan.Segments[0].Scale-plan.Scale) > 0.000001 {
		return fmt.Errorf("alignment plan global mapping does not match its first segment")
	}
	for i, segment := range plan.Segments {
		if segment.TargetStartMS < 0 || segment.TargetEndMS <= segment.TargetStartMS {
			return fmt.Errorf("alignment plan segment %d has invalid target bounds", i+1)
		}
		if !finiteSafeScale(segment.Scale) {
			return fmt.Errorf("alignment plan segment %d has unsafe scale %.9f", i+1, segment.Scale)
		}
		if math.Abs(segment.Scale-plan.Scale) > 0.000001 {
			return fmt.Errorf("alignment plan segment %d does not share the verified global clock scale", i+1)
		}
		expectedStart := mapPlanMS(segment, segment.TargetStartMS)
		expectedEnd := mapPlanMS(segment, segment.TargetEndMS)
		if absInt(expectedStart-segment.ReferenceStartMS) > 2 || absInt(expectedEnd-segment.ReferenceEndMS) > 2 || expectedEnd <= expectedStart {
			return fmt.Errorf("alignment plan segment %d reference bounds do not match its affine map", i+1)
		}
		if i == 0 {
			continue
		}
		previous := plan.Segments[i-1]
		gap := plan.Gaps[i-1]
		if segment.TargetStartMS < previous.TargetEndMS {
			return fmt.Errorf("alignment plan segments %d and %d overlap", i, i+1)
		}
		if gap.TargetAtMS != previous.TargetEndMS || gap.ReferenceBeforeMS != previous.ReferenceEndMS || gap.ReferenceAfterMS != segment.ReferenceStartMS {
			return fmt.Errorf("alignment plan gap %d is inconsistent with adjacent segment bounds", i)
		}
		if gap.DurationMS <= 0 || gap.DurationMS != absInt(gap.DeltaMS) {
			return fmt.Errorf("alignment plan gap %d has invalid duration/delta", i)
		}
		switch gap.Action {
		case "insert_silence":
			if gap.DeltaMS <= 0 || segment.TargetStartMS != gap.TargetAtMS || gap.ReferenceAfterMS-gap.ReferenceBeforeMS != gap.DeltaMS {
				return fmt.Errorf("alignment plan gap %d is not a valid reference-only interval", i)
			}
		case "remove_target":
			expectedResume := gap.TargetAtMS + int(math.Round(float64(gap.DurationMS)/segment.Scale))
			if gap.DeltaMS >= 0 || absInt(segment.TargetStartMS-expectedResume) > 2 || absInt(gap.ReferenceAfterMS-gap.ReferenceBeforeMS) > 2 {
				return fmt.Errorf("alignment plan gap %d is not a valid target-only interval", i)
			}
		default:
			return fmt.Errorf("alignment plan gap %d has unknown action %q", i, gap.Action)
		}
	}
	return nil
}

func finiteSafeScale(scale float64) bool {
	return !math.IsNaN(scale) && !math.IsInf(scale, 0) && scale >= 0.8 && scale <= 1.2
}

func sameDuration(expected, actual float64) bool {
	if expected <= 0 || actual <= 0 {
		return false
	}
	tolerance := math.Max(0.25, expected*0.0001)
	return math.Abs(expected-actual) <= tolerance
}

func sameSourceDuration(expected, actual float64) bool {
	if expected <= 0 || actual <= 0 {
		return false
	}
	return math.Abs(expected-actual) <= math.Max(2, expected*0.001)
}

func mapPlanMS(segment timeline.Segment, targetMS int) int {
	return int(math.Round(float64(targetMS)*segment.Scale)) + segment.OffsetMS
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeAlignmentPlan(path string, plan alignmentPlan, overwrite bool) error {
	if err := validateAlignmentPlan(plan, plan.Mode); err != nil {
		return fmt.Errorf("refuse to write invalid alignment plan: %w", err)
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("alignment plan already exists: %s (use --overwrite to replace it)", path)
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".redsync-plan-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(plan); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if overwrite {
		_ = os.Remove(path)
	}
	return os.Rename(tmpName, path)
}

func planFromDrift(referencePath, anchorPath string, referenceDuration, anchorDuration float64, drift rsync.Drift, verification *standaloneVerification) (alignmentPlan, error) {
	return newAlignmentPlan("audio", referencePath, anchorPath, referenceDuration, anchorDuration, drift.DelayMS, drift.Factor(), drift.Score, drift.Samples, drift.ResidualMS, drift.Segments, drift.Gaps, verification)
}

func planFromSubtitle(referencePath, anchorPath string, referenceDuration, anchorDuration float64, alignment subtitle.Alignment, verification *standaloneVerification) (alignmentPlan, error) {
	return newAlignmentPlan("subtitles", referencePath, anchorPath, referenceDuration, anchorDuration, alignment.OffsetMS, alignment.Scale, alignment.Score, alignment.Samples, alignment.ResidualMS, alignment.Segments, alignment.Gaps, verification)
}

func newAlignmentPlan(mode, referencePath, anchorPath string, referenceDuration, anchorDuration float64, syncMS int, scale, score float64, samples, residualMS int, segments []timeline.Segment, gaps []timeline.Gap, verification *standaloneVerification) (alignmentPlan, error) {
	if verification == nil || !verification.Passed {
		return alignmentPlan{}, fmt.Errorf("alignment plan can only be exported after verification passes")
	}
	digest, err := fileSHA256(referencePath)
	if err != nil {
		return alignmentPlan{}, err
	}
	return alignmentPlan{
		SchemaVersion: alignmentPlanSchemaVersion, Mode: mode, Verified: true,
		ReferenceBasename: filepath.Base(referencePath), ReferenceSHA256: digest,
		ReferenceDurationSeconds: referenceDuration, AnchorBasename: filepath.Base(anchorPath), AnchorDurationSeconds: anchorDuration,
		SyncMS: syncMS, Scale: scale, Score: score, Samples: samples, ResidualMS: residualMS,
		Segments: nonNilSegments(segments), Gaps: nonNilGaps(gaps),
		Verification: verificationGate{Passed: verification.Passed, SyncMS: verification.SyncMS, Scale: verification.Scale, DriftPPM: verification.DriftPPM, ResidualMS: verification.ResidualMS},
	}, nil
}

func (plan alignmentPlan) drift() rsync.Drift {
	return rsync.Drift{
		DelayMS: plan.SyncMS, Scale: plan.Scale, Score: plan.Score, Samples: plan.Samples, ResidualMS: plan.ResidualMS,
		Segments: append([]timeline.Segment(nil), plan.Segments...), Gaps: append([]timeline.Gap(nil), plan.Gaps...),
	}
}

func (plan alignmentPlan) subtitleAlignment(referenceCues, targetCues int) subtitle.Alignment {
	return subtitle.Alignment{
		Method: "plan", OffsetMS: plan.SyncMS, Scale: plan.Scale, Score: plan.Score,
		ReferenceCues: referenceCues, TargetCues: targetCues, Samples: plan.Samples, ResidualMS: plan.ResidualMS,
		Segments: append([]timeline.Segment(nil), plan.Segments...), Gaps: append([]timeline.Gap(nil), plan.Gaps...),
	}
}
