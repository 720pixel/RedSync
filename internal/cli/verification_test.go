package cli

import (
	"context"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/subtitle"
	rsync "github.com/720pixel/RedSync/internal/sync"
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
	failures := subtitleVerificationFailures(-68, 152, 215, 1000, 120, 250, 500, maxSemanticSubtitleDurationDeltaMS, nil)
	if len(failures) != 0 {
		t.Fatalf("bounded semantic verification unexpectedly failed: %#v", failures)
	}
	failures = subtitleVerificationFailures(-121, 251, 501, 59931, 120, 250, 500, maxSemanticSubtitleDurationDeltaMS, []timeline.Gap{{DurationMS: 800}})
	if len(failures) != 5 {
		t.Fatalf("failure reasons = %#v", failures)
	}
}

func TestSemanticVerificationResidualMatchesAlignmentSafetyBound(t *testing.T) {
	failures := subtitleVerificationFailures(-1, 0, 893, -1290, 120, 250, maxSemanticSubtitleResidualMS, maxSemanticSubtitleDurationDeltaMS, nil)
	if len(failures) != 0 {
		t.Fatalf("valid semantic cue split/merge jitter was rejected: %#v", failures)
	}
	failures = subtitleVerificationFailures(0, 0, maxSemanticSubtitleResidualMS+1, 0, 120, 250, maxSemanticSubtitleResidualMS, maxSemanticSubtitleDurationDeltaMS, nil)
	if len(failures) != 1 {
		t.Fatalf("unsafe semantic residual was accepted: %#v", failures)
	}
}

func TestSemanticVerificationRejectsUnmatchedMinuteAtEnd(t *testing.T) {
	failures := subtitleVerificationFailures(0, 0, 204, 59931, 120, 250, maxSemanticSubtitleResidualMS, maxSemanticSubtitleDurationDeltaMS, nil)
	if len(failures) != 1 || !strings.Contains(failures[0], "duration delta") {
		t.Fatalf("unmatched one-minute ending was accepted: %#v", failures)
	}
}

func TestSubtitleProgrammeCueEndIgnoresIsolatedTranslationCredit(t *testing.T) {
	cues := []subtitle.Cue{
		{Start: 48*time.Minute + 33*time.Second, End: 48*time.Minute + 41*time.Second},
		{Start: 49*time.Minute + 48*time.Second, End: 49*time.Minute + 51*time.Second},
	}
	if got, want := subtitleProgrammeCueEnd(cues), float64(48*60+41); got != want {
		t.Fatalf("programme end = %.3f, want %.3f", got, want)
	}
	cues[1].Start = 48*time.Minute + 44*time.Second
	cues[1].End = 48*time.Minute + 51*time.Second
	if got, want := subtitleProgrammeCueEnd(cues), float64(48*60+51); got != want {
		t.Fatalf("adjacent caption credit was stripped: %.3f, want %.3f", got, want)
	}
}

func TestStandaloneSubtitleSafetyDefaultsAndIndependentMatchers(t *testing.T) {
	cmd := standaloneSyncCmd()
	flag := cmd.Flags().Lookup("max-segments")
	if flag == nil || flag.DefValue != "16" {
		t.Fatalf("max-segments default = %#v, want 16", flag)
	}
	f := &standaloneFlags{}
	primary, ok := newCodexSemanticMatcherVariant(f, 0).(*subtitle.CodexAnchorMatcher)
	if !ok {
		t.Fatal("primary matcher has unexpected implementation")
	}
	verification := newCodexSemanticMatcherVariant(f, 1).(*subtitle.CodexAnchorMatcher)
	if primary.MaxAnchors != 40 || verification.MaxAnchors != 40 || primary.CueSelectionVariant == verification.CueSelectionVariant {
		t.Fatalf("semantic matchers are not independently sampled: primary=%+v verification=%+v", primary, verification)
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

func TestComposeAudioVerificationResidualAddsMissedTargetCut(t *testing.T) {
	base := rsync.Drift{
		DelayMS: 1000, Scale: 1.04, Score: 8, Samples: 20,
		Segments: []timeline.Segment{{
			TargetStartMS: 0, TargetEndMS: 10_000, ReferenceStartMS: 1000, ReferenceEndMS: 11_400,
			OffsetMS: 1000, Scale: 1.04, Score: 8, Samples: 20,
		}},
	}
	residual := rsync.Drift{
		DelayMS: 400, Scale: 1, Score: 7, Samples: 12,
		Segments: []timeline.Segment{
			{TargetStartMS: 0, TargetEndMS: 5000, ReferenceStartMS: 400, ReferenceEndMS: 5400, OffsetMS: 400, Scale: 1, Score: 7, Samples: 6},
			{TargetStartMS: 5400, TargetEndMS: 11_400, ReferenceStartMS: 5400, ReferenceEndMS: 11_400, OffsetMS: 0, Scale: 1, Score: 7, Samples: 6},
		},
		Gaps: []timeline.Gap{{TargetAtMS: 5000, ReferenceBeforeMS: 5400, ReferenceAfterMS: 5400, DeltaMS: -400, DurationMS: 400, Action: "remove_target"}},
	}
	got, err := composeAudioVerificationResidual(base, residual)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Segments) != 2 || len(got.Gaps) != 1 {
		t.Fatalf("composed audio timeline = segments %+v gaps %+v", got.Segments, got.Gaps)
	}
	if got.DelayMS != 1400 || math.Abs(got.Scale-1.04) > 0.0000001 || got.Gaps[0].Action != "remove_target" || got.Gaps[0].DurationMS != 400 {
		t.Fatalf("composed audio residual = %+v", got)
	}
	if base.DelayMS != 1000 || len(base.Gaps) != 0 {
		t.Fatalf("composition mutated its input: %+v", base)
	}
}

func TestBoundedDubAudioVerificationDoesNotRelaxMaterialErrors(t *testing.T) {
	near := rsync.Drift{
		DelayMS: -110, Scale: 1, Score: 5.16, Samples: 20, ResidualMS: 68,
		Gaps: []timeline.Gap{{DurationMS: 143, DeltaMS: -143, Action: "remove_target"}},
	}
	if !boundedDubAudioVerification(near, 22) {
		t.Fatal("bounded cross-language waveform jitter was rejected")
	}
	for name, unsafe := range map[string]rsync.Drift{
		"offset":   {DelayMS: 121, Scale: 1, Score: 6, Samples: 20},
		"drift":    {Scale: 1.000251, Score: 6, Samples: 20},
		"residual": {Scale: 1, Score: 6, Samples: 20, ResidualMS: 151},
		"gap":      {Scale: 1, Score: 6, Samples: 20, Gaps: []timeline.Gap{{DurationMS: 251}}},
		"score":    {Scale: 1, Score: 4.99, Samples: 20},
		"samples":  {Scale: 1, Score: 6, Samples: 11},
	} {
		if boundedDubAudioVerification(unsafe, 22) {
			t.Fatalf("unsafe dubbed-audio %s mismatch was accepted: %+v", name, unsafe)
		}
	}
	if boundedDubAudioVerification(near, 101) {
		t.Fatal("unsafe duration mismatch was accepted")
	}
}

func TestDistinctAudioLanguagesNormalizesISOAliases(t *testing.T) {
	if distinctAudioLanguages("en", "eng") || distinctAudioLanguages("fra", "fre") {
		t.Fatal("equivalent ISO audio language aliases were treated as cross-language")
	}
	if !distinctAudioLanguages("eng", "deu") {
		t.Fatal("English and German audio were treated as the same language")
	}
	if distinctAudioLanguages("und", "deu") || distinctAudioLanguages("", "deu") {
		t.Fatal("unknown audio metadata was treated as proof of cross-language audio")
	}
}

func TestAudioTimelineMatchesPlanRequiresExactStrongRender(t *testing.T) {
	plan := alignmentPlan{
		SyncMS: 1000, Scale: 1,
		Segments: []timeline.Segment{{
			TargetStartMS: 0, TargetEndMS: 1_200_000, ReferenceStartMS: 1000, ReferenceEndMS: 1_201_000,
			OffsetMS: 1000, Scale: 1, Score: 8.5, Samples: 25, ResidualMS: 10,
		}},
		Gaps: []timeline.Gap{},
	}
	observed := rsync.Drift{
		DelayMS: 1040, Scale: 1.00005, Score: 8, Samples: 25, ResidualMS: 30,
		Segments: []timeline.Segment{{TargetStartMS: 0, TargetEndMS: 1_200_000, OffsetMS: 1040, Scale: 1.00005}},
		Gaps:     []timeline.Gap{},
	}
	if !audioTimelineMatchesPlan(plan, observed) {
		t.Fatal("matching planned render was rejected")
	}
	for name, mutate := range map[string]func(*rsync.Drift){
		"few anchors":    func(d *rsync.Drift) { d.Samples = 11 },
		"weak waveform":  func(d *rsync.Drift) { d.Score = 3.9 },
		"large residual": func(d *rsync.Drift) { d.ResidualMS = 101 },
		"large offset":   func(d *rsync.Drift) { d.DelayMS = 1081; d.Segments[0].OffsetMS = 1081 },
		"large drift":    func(d *rsync.Drift) { d.Scale = 1.000076; d.Segments[0].Scale = 1.000076 },
		"wrong boundary": func(d *rsync.Drift) { d.Segments[0].TargetEndMS += 251 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := observed
			candidate.Segments = append([]timeline.Segment(nil), observed.Segments...)
			mutate(&candidate)
			if audioTimelineMatchesPlan(plan, candidate) {
				t.Fatalf("unsafe planned render accepted: %+v", candidate)
			}
		})
	}
}

func TestPlannedAudioVerificationMaxOffsetAllowsExactVerifiedPlan(t *testing.T) {
	for name, tc := range map[string]struct {
		configured float64
		expected   float64
		want       float64
	}{
		"ordinary offset keeps narrow limit": {configured: 300, expected: 12, want: 30},
		"positive offset beyond limit":       {configured: 300, expected: 33.616, want: 33.616},
		"negative offset beyond limit":       {configured: 300, expected: -41.250, want: 41.250},
		"configured lower limit":             {configured: 20, expected: 12, want: 20},
	} {
		t.Run(name, func(t *testing.T) {
			if got := plannedAudioVerificationMaxOffset(tc.configured, tc.expected); got != tc.want {
				t.Fatalf("verification max offset = %.3f, want %.3f", got, tc.want)
			}
		})
	}
}

func TestPlannedAudioTimelineSegmentProbeFallbackRequiresStrongTerminalAgreement(t *testing.T) {
	plan := alignmentPlan{Scale: 1, Segments: []timeline.Segment{
		{TargetStartMS: 0, TargetEndMS: 60_000, OffsetMS: 1_000, Scale: 1},
		{TargetStartMS: 60_500, TargetEndMS: 120_000, OffsetMS: 500, Scale: 1},
	}}
	observed := rsync.Drift{Scale: 1, Score: 8, Samples: 20, ResidualMS: 10, Segments: []timeline.Segment{
		{TargetStartMS: 0, TargetEndMS: 90_000, OffsetMS: 1_000, Scale: 1},
		{TargetStartMS: 90_500, TargetEndMS: 120_000, OffsetMS: 500, Scale: 1},
	}}
	if !plannedAudioTimelineSupportsSegmentProbes(plan, observed) {
		t.Fatal("strong ambiguous timeline was not eligible for independent segment probes")
	}
	observed.Segments[1].OffsetMS = 600
	if plannedAudioTimelineSupportsSegmentProbes(plan, observed) {
		t.Fatal("timeline with a wrong terminal mapping was eligible for fallback")
	}
}

func TestPlannedAudioOutputReplay(t *testing.T) {
	referencePath := os.Getenv("REDSYNC_PLAN_REPLAY_REFERENCE")
	targetPath := os.Getenv("REDSYNC_PLAN_REPLAY_TARGET")
	outputPath := os.Getenv("REDSYNC_PLAN_REPLAY_OUTPUT")
	anchorPath := os.Getenv("REDSYNC_PLAN_REPLAY_ANCHOR")
	planPath := os.Getenv("REDSYNC_PLAN_REPLAY_PLAN")
	if referencePath == "" || targetPath == "" || outputPath == "" || anchorPath == "" || planPath == "" {
		t.Skip("set REDSYNC_PLAN_REPLAY_* paths for a local planned-audio verification replay")
	}
	trackIndex, err := strconv.Atoi(os.Getenv("REDSYNC_PLAN_REPLAY_TARGET_TRACK"))
	if err != nil {
		t.Fatal("REDSYNC_PLAN_REPLAY_TARGET_TRACK must be an integer")
	}
	ctx := context.Background()
	reference, err := media.Probe(ctx, referencePath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := readAlignmentPlan(planPath, "audio", referencePath, reference.Duration)
	if err != nil {
		t.Fatal(err)
	}
	target, err := media.Probe(ctx, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	targetTrack, err := chooseAudioTrack(target, trackIndex)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := media.Probe(ctx, anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	verification, observed, err := verifyPlannedAudioOutput(ctx, target, targetTrack, anchor, outputPath, plan, &standaloneFlags{
		maxOffset: 300, minGap: .35, maxSegments: 16,
	})
	if err != nil || verification == nil || !verification.Passed {
		t.Fatalf("planned output replay failed: verification=%+v observed=%+v err=%v", verification, observed, err)
	}
	t.Logf("planned output verified: verification=%+v observed=%+v", verification, observed)
}

func TestStrongDistributedDubTimelineRejectsWeakOrLargeEdits(t *testing.T) {
	strong := rsync.Drift{
		DelayMS: 28000, Scale: 1.04262, Score: 6.88, Samples: 23, ResidualMS: 13,
		Segments: []timeline.Segment{
			{TargetStartMS: 0, TargetEndMS: 600000, ReferenceStartMS: 28000, ReferenceEndMS: 653572, OffsetMS: 28000, Scale: 1.04262, Score: 7, Samples: 5, ResidualMS: 10},
			{TargetStartMS: 600480, TargetEndMS: 1200000, ReferenceStartMS: 653572, ReferenceEndMS: 1278671, OffsetMS: 27500, Scale: 1.04262, Score: 7, Samples: 6, ResidualMS: 12},
		},
		Gaps: []timeline.Gap{{TargetAtMS: 600000, ReferenceBeforeMS: 653572, ReferenceAfterMS: 653572, DeltaMS: -500, DurationMS: 500, Action: "remove_target"}},
	}
	if !strongDistributedDubTimeline(strong, 1278.671, 1200) {
		t.Fatal("strong distributed dubbed timeline was rejected")
	}
	weak := strong
	weak.Samples = 19
	if strongDistributedDubTimeline(weak, 1278.671, 1200) {
		t.Fatal("sparse primary timeline was accepted")
	}
	largeEdit := strong
	largeEdit.Gaps = append([]timeline.Gap(nil), strong.Gaps...)
	largeEdit.Gaps[0].DurationMS = 1001
	largeEdit.Gaps[0].DeltaMS = -1001
	if strongDistributedDubTimeline(largeEdit, 1278.671, 1200) {
		t.Fatal("large unverified edit was accepted")
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
