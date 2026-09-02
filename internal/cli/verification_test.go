package cli

import (
	"math"
	"reflect"
	"testing"

	"github.com/720pixel/RedSync/internal/subtitle"
	"github.com/720pixel/RedSync/internal/timeline"
)

func TestSemanticVerificationToleratesOnlySmallBoundedCueJitter(t *testing.T) {
	small := []timeline.Gap{{DurationMS: 211, Action: "remove_target"}}
	remaining, tolerated := crossLanguageVerificationGaps(small, false)
	if len(remaining) != 0 || !reflect.DeepEqual(tolerated, small) {
		t.Fatalf("small gap policy: remaining=%#v tolerated=%#v", remaining, tolerated)
	}

	for _, blocking := range [][]timeline.Gap{
		{{DurationMS: 501}},
		{{DurationMS: 400}, {DurationMS: 351}},
		{{DurationMS: 100}, {DurationMS: 100}, {DurationMS: 100}},
	} {
		remaining, tolerated = crossLanguageVerificationGaps(blocking, false)
		if len(remaining) != len(blocking) || len(tolerated) != 0 {
			t.Fatalf("blocking gap policy: remaining=%#v tolerated=%#v", remaining, tolerated)
		}
	}
}

func TestSemanticVerificationUsesSemanticCueJitterEnvelope(t *testing.T) {
	semanticJitter := []timeline.Gap{{DurationMS: 844, Action: "insert_silence"}}
	remaining, tolerated := crossLanguageVerificationGaps(semanticJitter, true)
	if len(remaining) != 0 || !reflect.DeepEqual(tolerated, semanticJitter) {
		t.Fatalf("semantic cue jitter was rejected: remaining=%#v tolerated=%#v", remaining, tolerated)
	}
	remaining, tolerated = crossLanguageVerificationGaps([]timeline.Gap{{DurationMS: 1001}}, true)
	if len(remaining) != 1 || len(tolerated) != 0 {
		t.Fatalf("unsafe semantic gap was tolerated: remaining=%#v tolerated=%#v", remaining, tolerated)
	}
}

func TestSubtitleVerificationFailuresNamesOnlyExceededBounds(t *testing.T) {
	failures := subtitleVerificationFailures(-68, 152, 215, 120, 250, 500, nil)
	if len(failures) != 0 {
		t.Fatalf("bounded semantic verification unexpectedly failed: %#v", failures)
	}
	failures = subtitleVerificationFailures(-121, 251, 501, 120, 250, 500, []timeline.Gap{{DurationMS: 800}})
	if len(failures) != 4 {
		t.Fatalf("failure reasons = %#v", failures)
	}
}

func TestSemanticVerificationResidualMatchesAlignmentSafetyBound(t *testing.T) {
	failures := subtitleVerificationFailures(-1, 0, 893, 120, 250, maxSemanticSubtitleResidualMS, nil)
	if len(failures) != 0 {
		t.Fatalf("valid semantic cue split/merge jitter was rejected: %#v", failures)
	}
	failures = subtitleVerificationFailures(0, 0, maxSemanticSubtitleResidualMS+1, 120, 250, maxSemanticSubtitleResidualMS, nil)
	if len(failures) != 1 {
		t.Fatalf("unsafe semantic residual was accepted: %#v", failures)
	}
}

func TestStrongCrossLanguageActivityRequiresDistributedPreciseEvidence(t *testing.T) {
	good := subtitle.Alignment{Score: 0.875, Samples: 19, ResidualMS: 57, Scale: 0.9998}
	if !strongCrossLanguageActivity(good, 0.1) {
		t.Fatal("strong deterministic activity was not accepted")
	}
	for name, weak := range map[string]subtitle.Alignment{
		"score":    {Score: 0.69, Samples: 19, ResidualMS: 57, Scale: 1},
		"samples":  {Score: 0.90, Samples: 7, ResidualMS: 57, Scale: 1},
		"residual": {Score: 0.90, Samples: 19, ResidualMS: 351, Scale: 1},
	} {
		if strongCrossLanguageActivity(weak, 0.1) {
			t.Fatalf("weak %s evidence was accepted", name)
		}
	}
}

func TestBetterSubtitleVerificationPrefersPassThenFewerFailures(t *testing.T) {
	failed := &standaloneVerification{FailureReasons: []string{"offset", "gap"}, SyncMS: 130, ResidualMS: 600}
	closer := &standaloneVerification{FailureReasons: []string{"offset"}, SyncMS: 125, ResidualMS: 200}
	passed := &standaloneVerification{Passed: true, SyncMS: 30, ResidualMS: 80}
	if !betterSubtitleVerification(passed, closer) || betterSubtitleVerification(closer, passed) {
		t.Fatal("a passing candidate must always win")
	}
	if !betterSubtitleVerification(closer, failed) {
		t.Fatal("candidate with fewer failed safety bounds should win")
	}
}

func TestComposeGlobalSubtitleResidualPreservesPiecewisePlan(t *testing.T) {
	base := subtitle.Alignment{
		Method: "semantic-codex", OffsetMS: 2000, Scale: 0.99,
		Segments: []timeline.Segment{
			{TargetStartMS: 0, TargetEndMS: 10000, ReferenceStartMS: 2000, ReferenceEndMS: 11900, OffsetMS: 2000, Scale: 0.99},
			{TargetStartMS: 10000, TargetEndMS: 20000, ReferenceStartMS: 12400, ReferenceEndMS: 22300, OffsetMS: 2500, Scale: 0.99},
		},
		Gaps: []timeline.Gap{{TargetAtMS: 10000, ReferenceBeforeMS: 11900, ReferenceAfterMS: 12400, DeltaMS: 500, DurationMS: 500, Action: "insert_silence"}},
	}
	got := composeGlobalSubtitleResidual(base, subtitle.Alignment{OffsetMS: -100, Scale: 1.001})
	if got.Method != "semantic-codex+residual" || got.OffsetMS != 1902 || math.Abs(got.Scale-0.99099) > 0.0000001 {
		t.Fatalf("global composition = %+v", got)
	}
	if got.Segments[1].OffsetMS != 2402 || got.Segments[1].ReferenceStartMS != 12312 || got.Gaps[0].DurationMS != 500 {
		t.Fatalf("piecewise composition = segments %+v gaps %+v", got.Segments, got.Gaps)
	}
	if base.Segments[0].OffsetMS != 2000 || base.Gaps[0].ReferenceBeforeMS != 11900 {
		t.Fatalf("composition mutated its input: %+v", base)
	}

	base.Gaps[0] = timeline.Gap{TargetAtMS: 10000, ReferenceBeforeMS: 11900, ReferenceAfterMS: 11900, DeltaMS: -500, DurationMS: 500, Action: "remove_target"}
	got = composeGlobalSubtitleResidual(base, subtitle.Alignment{OffsetMS: -100, Scale: 1.001})
	if got.Gaps[0].DeltaMS != -500 || got.Gaps[0].DurationMS != 500 || got.Gaps[0].Action != "remove_target" {
		t.Fatalf("target-only gap composition = %+v", got.Gaps[0])
	}
}
