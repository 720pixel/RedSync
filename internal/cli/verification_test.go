package cli

import (
	"reflect"
	"testing"

	"github.com/720pixel/RedSync/internal/subtitle"
	"github.com/720pixel/RedSync/internal/timeline"
)

func TestSemanticVerificationToleratesOnlySmallBoundedCueJitter(t *testing.T) {
	small := []timeline.Gap{{DurationMS: 211, Action: "remove_target"}}
	remaining, tolerated := semanticVerificationGaps(small)
	if len(remaining) != 0 || !reflect.DeepEqual(tolerated, small) {
		t.Fatalf("small gap policy: remaining=%#v tolerated=%#v", remaining, tolerated)
	}

	for _, blocking := range [][]timeline.Gap{
		{{DurationMS: 501}},
		{{DurationMS: 400}, {DurationMS: 351}},
		{{DurationMS: 100}, {DurationMS: 100}, {DurationMS: 100}},
	} {
		remaining, tolerated = semanticVerificationGaps(blocking)
		if len(remaining) != len(blocking) || len(tolerated) != 0 {
			t.Fatalf("blocking gap policy: remaining=%#v tolerated=%#v", remaining, tolerated)
		}
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
