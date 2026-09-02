package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/720pixel/RedSync/internal/subtitle"
	"github.com/720pixel/RedSync/internal/timeline"
)

func TestSiblingVerificationReferenceRequiresVerifiedPlanRendering(t *testing.T) {
	tests := []struct {
		name string
		flag standaloneFlags
		want string
	}{
		{
			name: "plan required",
			flag: standaloneFlags{verificationReference: "anchor.mka", verify: true, minGap: 0.35, maxSegments: 8, semanticCodexTimeout: time.Second},
			want: "requires --alignment-plan",
		},
		{
			name: "render required",
			flag: standaloneFlags{alignmentPlan: "plan.json", verificationReference: "anchor.mka", verify: true, dryRun: true, minGap: 0.35, maxSegments: 8, semanticCodexTimeout: time.Second},
			want: "requires output rendering",
		},
		{
			name: "verification required",
			flag: standaloneFlags{alignmentPlan: "plan.json", verificationReference: "anchor.mka", minGap: 0.35, maxSegments: 8, semanticCodexTimeout: time.Second},
			want: "--verify=true",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runStandaloneSync(context.Background(), "missing-reference", []string{"missing-target"}, &tc.flag, false, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerifyPlannedSubtitleOutputChecksExactTransformedCues(t *testing.T) {
	ctx := context.Background()
	expected := []subtitle.Cue{
		{Start: 1500 * time.Millisecond, End: 2750 * time.Millisecond, Text: []string{"Pierwsza linia"}},
		{Start: 5 * time.Second, End: 6500 * time.Millisecond, Text: []string{"Druga", "linia"}},
	}
	output := filepath.Join(t.TempDir(), "polish.vtt")
	if err := subtitle.Write(ctx, output, expected, true); err != nil {
		t.Fatal(err)
	}
	verification, err := verifyPlannedSubtitleOutput(ctx, expected, output)
	if err != nil || !verification.Passed || verification.SyncMS != 0 || verification.ResidualMS != 0 {
		t.Fatalf("exact planned output rejected: verification=%+v err=%v", verification, err)
	}

	shifted := append([]subtitle.Cue(nil), expected...)
	shifted[1].Start += 200 * time.Millisecond
	if err := subtitle.Write(ctx, output, shifted, true); err != nil {
		t.Fatal(err)
	}
	verification, err = verifyPlannedSubtitleOutput(ctx, expected, output)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Passed || verification.ResidualMS != 200 {
		t.Fatalf("shifted planned output accepted: %+v", verification)
	}
}

func validTestAlignmentPlan(t *testing.T) (alignmentPlan, string) {
	t.Helper()
	dir := t.TempDir()
	reference := filepath.Join(dir, "reference.m4a")
	if err := os.WriteFile(reference, []byte("exact reference bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(reference)
	if err != nil {
		t.Fatal(err)
	}
	segments := []timeline.Segment{
		{TargetStartMS: 0, TargetEndMS: 60_000, ReferenceStartMS: -2_000, ReferenceEndMS: 58_000, OffsetMS: -2_000, Scale: 1, Samples: 8},
		{TargetStartMS: 60_000, TargetEndMS: 120_000, ReferenceStartMS: 63_000, ReferenceEndMS: 123_000, OffsetMS: 3_000, Scale: 1, Samples: 8},
	}
	gaps := []timeline.Gap{{TargetAtMS: 60_000, ReferenceBeforeMS: 58_000, ReferenceAfterMS: 63_000, DeltaMS: 5_000, DurationMS: 5_000, Action: "insert_silence"}}
	return alignmentPlan{
		SchemaVersion: 1, Mode: "audio", Verified: true,
		ReferenceBasename: filepath.Base(reference), ReferenceSHA256: digest,
		ReferenceDurationSeconds: 123, AnchorBasename: "english.mka", AnchorDurationSeconds: 120,
		SyncMS: -2_000, Scale: 1, Score: 8.5, Samples: 16, ResidualMS: 12,
		Segments: segments, Gaps: gaps,
		Verification: verificationGate{Passed: true, Scale: 1, ResidualMS: 20},
	}, reference
}

func TestAlignmentPlanRoundTripPreservesPiecewiseMapping(t *testing.T) {
	plan, reference := validTestAlignmentPlan(t)
	path := filepath.Join(t.TempDir(), "anchor-plan.json")
	if err := writeAlignmentPlan(path, plan, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := readAlignmentPlan(path, "audio", reference, 123)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Segments) != 2 || len(loaded.Gaps) != 1 || loaded.Gaps[0].Action != "insert_silence" {
		t.Fatalf("piecewise mapping lost: %+v", loaded)
	}
	drift := loaded.drift()
	if len(drift.Segments) != 2 || drift.Segments[1].OffsetMS != 3_000 || drift.Gaps[0].DurationMS != 5_000 {
		t.Fatalf("drift conversion lost exact plan: %+v", drift)
	}
}

func TestAlignmentPlanValidatesTargetOnlyGap(t *testing.T) {
	plan, _ := validTestAlignmentPlan(t)
	plan.Segments = []timeline.Segment{
		{TargetStartMS: 0, TargetEndMS: 60_000, ReferenceStartMS: -2_000, ReferenceEndMS: 58_000, OffsetMS: -2_000, Scale: 1},
		{TargetStartMS: 65_000, TargetEndMS: 120_000, ReferenceStartMS: 58_000, ReferenceEndMS: 113_000, OffsetMS: -7_000, Scale: 1},
	}
	plan.Gaps = []timeline.Gap{{TargetAtMS: 60_000, ReferenceBeforeMS: 58_000, ReferenceAfterMS: 58_000, DeltaMS: -5_000, DurationMS: 5_000, Action: "remove_target"}}
	if err := validateAlignmentPlan(plan, "audio"); err != nil {
		t.Fatalf("valid target-only plan rejected: %v", err)
	}
	plan.Segments[1].TargetStartMS = 64_000
	plan.Segments[1].ReferenceStartMS = 57_000
	if err := validateAlignmentPlan(plan, "audio"); err == nil {
		t.Fatalf("unsafe target resume accepted: %v", err)
	}
}

func TestAlignmentPlanSubtitleConversionRendersExactSegments(t *testing.T) {
	plan, _ := validTestAlignmentPlan(t)
	plan.Mode = "subtitles"
	alignment := plan.subtitleAlignment(20, 20)
	cues := []subtitle.Cue{
		{Start: 10 * time.Second, End: 11 * time.Second, Text: []string{"before"}},
		{Start: 70 * time.Second, End: 71 * time.Second, Text: []string{"after"}},
	}
	got := subtitle.Apply(cues, alignment)
	if len(got) != 2 || got[0].Start != 8*time.Second || got[1].Start != 73*time.Second {
		t.Fatalf("plan mapping not applied exactly: %#v", got)
	}
}

func TestAlignmentPlanRejectsUnverifiedAndWrongReference(t *testing.T) {
	plan, reference := validTestAlignmentPlan(t)
	plan.Verification.Passed = false
	if err := validateAlignmentPlan(plan, "audio"); err == nil || !strings.Contains(err.Error(), "not marked") {
		t.Fatalf("unverified plan accepted: %v", err)
	}
	plan.Verification.Passed = true
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := writeAlignmentPlan(path, plan, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reference, []byte("different reference"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readAlignmentPlan(path, "audio", reference, 123); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("wrong reference accepted: %v", err)
	}
}

func TestAlignmentPlanRejectsNonMonotonicSegments(t *testing.T) {
	plan, _ := validTestAlignmentPlan(t)
	plan.Segments[1].TargetStartMS = 59_000
	plan.Segments[1].ReferenceStartMS = 62_000
	if err := validateAlignmentPlan(plan, "audio"); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlapping plan accepted: %v", err)
	}
}

func TestAlignmentPlanRejectsUnknownJSONFields(t *testing.T) {
	plan, reference := validTestAlignmentPlan(t)
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := writeAlignmentPlan(path, plan, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "{", "{\n  \"surprise\": true,", 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readAlignmentPlan(path, "audio", reference, 123); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field accepted: %v", err)
	}
}

func TestAlignmentPlanRequiresSameSourceDurationForAudioSiblings(t *testing.T) {
	if !sameSourceDuration(3600, 3601.5) {
		t.Fatal("minor codec duration difference should be accepted")
	}
	if sameSourceDuration(3600, 3610) {
		t.Fatal("different source cut should be rejected")
	}
}

func TestSourceTimelineSubtitleAlignmentAddsVerifiedResidual(t *testing.T) {
	plan, _ := validTestAlignmentPlan(t)
	target := []subtitle.Cue{
		{Start: 10 * time.Second, End: 11 * time.Second, Text: []string{"one"}},
		{Start: 19 * time.Second, End: 21 * time.Second, Text: []string{"two"}},
		{Start: 37 * time.Second, End: 39 * time.Second, Text: []string{"three"}},
		{Start: 55 * time.Second, End: 57 * time.Second, Text: []string{"four"}},
		{Start: 65 * time.Second, End: 67 * time.Second, Text: []string{"five"}},
		{Start: 78 * time.Second, End: 81 * time.Second, Text: []string{"six"}},
		{Start: 96 * time.Second, End: 98 * time.Second, Text: []string{"seven"}},
		{Start: 115 * time.Second, End: 117 * time.Second, Text: []string{"eight"}},
	}
	wantAlignment := plan.subtitleAlignment(len(target), len(target))
	wantAlignment.OffsetMS += 300
	for i := range wantAlignment.Segments {
		wantAlignment.Segments[i].OffsetMS += 300
		wantAlignment.Segments[i].ReferenceStartMS += 300
		wantAlignment.Segments[i].ReferenceEndMS += 300
	}
	for i := range wantAlignment.Gaps {
		wantAlignment.Gaps[i].ReferenceBeforeMS += 300
		wantAlignment.Gaps[i].ReferenceAfterMS += 300
	}
	reference := subtitle.Apply(target, wantAlignment)

	got, err := subtitleAlignmentFromSourceTimeline(reference, target, plan, subtitle.AlignOptions{
		MaxOffsetSeconds: 30, MinScore: 0.1, MinGapSeconds: 0.35, MaxSegments: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "source_timeline_plan" || got.OffsetMS != plan.SyncMS+300 || len(got.Gaps) != len(plan.Gaps) {
		t.Fatalf("source timeline residual not preserved: %+v", got)
	}
	if rendered := subtitle.Apply(target, got); len(rendered) != len(reference) || rendered[0].Start != reference[0].Start || rendered[len(rendered)-1].End != reference[len(reference)-1].End {
		t.Fatalf("source timeline render differs from reference: got=%+v want=%+v", rendered, reference)
	}
}

func TestReadSourceTimelinePlanRejectsSubtitlePlan(t *testing.T) {
	plan, _ := validTestAlignmentPlan(t)
	plan.Mode = "subtitles"
	path := filepath.Join(t.TempDir(), "subtitle-plan.json")
	if err := writeAlignmentPlan(path, plan, false); err != nil {
		t.Fatal(err)
	}
	if _, err := readSourceTimelinePlan(path); err == nil || !strings.Contains(err.Error(), "cannot be used for audio") {
		t.Fatalf("subtitle source timeline accepted: %v", err)
	}
}
