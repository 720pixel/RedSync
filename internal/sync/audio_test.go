package sync

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/offset"
	"github.com/720pixel/RedSync/internal/timeline"
)

func TestRobustDelayLine(t *testing.T) {
	// y = x*0.96 - 3.2, so delay = -0.04*x - 3.2.
	anchors := []audioAnchor{
		{x: 0, delay: -3.200},
		{x: 100, delay: -7.198},
		{x: 300, delay: -15.204},
		{x: 600, delay: -27.199},
		{x: 900, delay: -39.201},
		{x: 1100, delay: 80}, // deliberate bad probe
	}
	slope, intercept, residual := robustDelayLine(anchors, 1100)
	if math.Abs(slope+0.04) > 0.00005 {
		t.Fatalf("slope = %.9f, want -0.04", slope)
	}
	if math.Abs(intercept+3.2) > 0.01 {
		t.Fatalf("intercept = %.6f, want -3.2", intercept)
	}
	if residual > 0.02 {
		t.Fatalf("residual = %.6f, want <= .02", residual)
	}
}

func TestFindSilenceStart(t *testing.T) {
	original := offsetDecode
	defer func() { offsetDecode = original }()
	offsetDecode = func(_ context.Context, _ string, _ int, _, duration float64) ([]float64, error) {
		const samplesPerSecond = 100
		out := make([]float64, int(duration*samplesPerSecond))
		for i := range out {
			second := float64(i) / samplesPerSecond
			if second < 6 || second >= 10 {
				out[i] = .25
			}
		}
		return out, nil
	}
	got, ok := findSilenceStart(context.Background(), "fixture", 0, 8, 8, 2)
	if !ok || math.Abs(got-6) > .06 {
		t.Fatalf("findSilenceStart = %.3f, %v; want 6.0, true", got, ok)
	}
}

func TestInitialAudioMatchRetriesKnownFPSScale(t *testing.T) {
	originalDecode, originalFind := offsetDecode, offsetFindScaled
	defer func() {
		offsetDecode, offsetFindScaled = originalDecode, originalFind
	}()
	offsetDecode = func(_ context.Context, _ string, _ int, _, _ float64) ([]float64, error) {
		return make([]float64, 512), nil
	}
	wantScale := 25 / (24000.0 / 1001)
	offsetFindScaled = func(_, _ []float64, scale float64) (offset.Result, error) {
		score := 3.7
		if math.Abs(scale-wantScale) < 0.000001 {
			score = 11
		}
		return offset.Result{Offset: -2.625, Score: score}, nil
	}

	delay, score, scale, err := initialAudioMatch(
		context.Background(), "ref", 0, "target", 0,
		155, 4, 1209.5, 1155.7,
	)
	if err != nil {
		t.Fatal(err)
	}
	if delay != -2625 || score != 11 || math.Abs(scale-wantScale) > 0.000001 {
		t.Fatalf("initialAudioMatch = %dms, %.2f, %.9f; want -2625ms, 11, %.9f", delay, score, scale, wantScale)
	}
}

func TestMeasureAudioContinuesPastWeakHeadSeedForFPSMismatch(t *testing.T) {
	originalDecode, originalFind := offsetDecode, offsetFindScaled
	defer func() {
		offsetDecode, offsetFindScaled = originalDecode, originalFind
	}()
	offsetDecode = func(_ context.Context, _ string, _ int, _, duration float64) ([]float64, error) {
		return make([]float64, int(math.Round(duration*10))), nil
	}
	const expectedScale = 0.99896
	offsetFindScaled = func(a, b []float64, scale float64) (offset.Result, error) {
		// The initial 155-second head probe is deliberately marginal at every
		// candidate speed. Shorter full-runtime probes are strong only when the
		// 23.976 -> 24 timing correction is applied.
		if len(a) > 1000 || len(b) > 1000 {
			return offset.Result{Score: 3.92}, nil
		}
		if math.Abs(scale-expectedScale) < 0.0002 {
			return offset.Result{Score: 8.1}, nil
		}
		return offset.Result{Score: 3.1}, nil
	}

	ref := media.File{Path: "reference", Duration: 7133.760, Audio: []media.Track{{Index: 0, Kind: media.Audio}}}
	target := media.File{Path: "target", Duration: 7141.184, Audio: []media.Track{{Index: 0, Kind: media.Audio}}}
	drift, err := MeasureAudio(context.Background(), ref, target, 0, 0, MeasureOptions{
		MinScore: 4, DisablePiecewise: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if drift.Score < 4 || drift.Samples < 4 {
		t.Fatalf("full-runtime evidence was not enforced: score=%.2f samples=%d", drift.Score, drift.Samples)
	}
	if math.Abs(drift.Factor()-expectedScale) > 0.0002 {
		t.Fatalf("scale = %.9f, want near %.9f", drift.Factor(), expectedScale)
	}
}

func TestRefineAudioBoundarySearchesOriginalBracket(t *testing.T) {
	originalDecode, originalFind := offsetDecode, offsetFindScaled
	defer func() {
		offsetDecode, offsetFindScaled = originalDecode, originalFind
	}()
	offsetDecode = func(_ context.Context, path string, _ int, start, duration float64) ([]float64, error) {
		const samplesPerSecond = 100
		out := make([]float64, int(math.Round(duration*samplesPerSecond)))
		for i := range out {
			at := start + float64(i)/samplesPerSecond
			if path != "target" || at < 345 || at >= 347 {
				out[i] = .25
			}
		}
		return out, nil
	}
	// Equal scores make the classifier keep choosing the old map, deliberately
	// collapsing its binary-search interval far away from the physical gap.
	offsetFindScaled = func(_, _ []float64, _ float64) (offset.Result, error) {
		return offset.Result{Score: 20}, nil
	}
	fit := timeline.Fit{
		Scale: 1,
		Segments: []timeline.Segment{
			{TargetStartMS: 0, TargetEndMS: 300_000, OffsetMS: 0, Scale: 1},
			{TargetStartMS: 302_000, TargetEndMS: 600_000, OffsetMS: -2_000, Scale: 1},
		},
		Gaps: []timeline.Gap{{TargetAtMS: 300_000, DeltaMS: -2_000, DurationMS: 2_000, Action: "remove_target"}},
	}
	ref := media.File{Path: "reference", Duration: 600}
	target := media.File{Path: "target", Duration: 600}
	refineAudioBoundary(context.Background(), ref, target, 0, 0, &fit, 0, 7)
	if math.Abs(float64(fit.Gaps[0].TargetAtMS-345_000)) > 60 {
		t.Fatalf("gap boundary = %dms, want 345000ms", fit.Gaps[0].TargetAtMS)
	}
	if math.Abs(float64(fit.Segments[1].TargetStartMS-347_000)) > 60 {
		t.Fatalf("resume boundary = %dms, want 347000ms", fit.Segments[1].TargetStartMS)
	}
}

func TestPiecewiseAudioFilterRepairsBothGapDirections(t *testing.T) {
	segments := []timeline.Segment{
		{TargetStartMS: 0, TargetEndMS: 100_000, OffsetMS: 0, Scale: 1},
		{TargetStartMS: 100_000, TargetEndMS: 202_000, OffsetMS: 5_000, Scale: 1},
		{TargetStartMS: 205_000, TargetEndMS: 300_000, OffsetMS: 2_000, Scale: 1},
	}
	gaps := []timeline.Gap{
		{TargetAtMS: 100_000, DeltaMS: 5_000, DurationMS: 5_000, Action: "insert_silence"},
		{TargetAtMS: 205_000, DeltaMS: -3_000, DurationMS: 3_000, Action: "remove_target"},
	}
	graph, err := piecewiseAudioFilter(2, segments, gaps, 302)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[0:2]asplit=3", "atrim=start=0.000000:end=100.000000",
		"adelay=105000:all=1", "atrim=start=100.000000:end=202.000000",
		"amix=inputs=3", "atrim=duration=302.000000",
	} {
		if !strings.Contains(graph, want) {
			t.Fatalf("filter graph missing %q:\n%s", want, graph)
		}
	}
}

func TestExactFPSRatioMapsTargetToReference(t *testing.T) {
	ref := media.Track{FPSNum: 24000, FPSDen: 1001}
	target := media.Track{FPSNum: 25, FPSDen: 1}
	if got := exactFPSRatio(ref, target); got != "1001/960" {
		t.Fatalf("exactFPSRatio = %s, want 1001/960", got)
	}
}

func TestDriftFactor(t *testing.T) {
	for _, tc := range []struct {
		d    Drift
		want float64
	}{
		{Drift{}, 1},
		{Drift{Scale: 0.96, Linear: "bad"}, 0.96},
		{Drift{Linear: "1001/960"}, 1001.0 / 960},
		{Drift{Linear: "1.001000000"}, 1.001},
	} {
		if got := tc.d.Factor(); math.Abs(got-tc.want) > 1e-10 {
			t.Fatalf("Factor() = %.12f, want %.12f", got, tc.want)
		}
	}
}

func TestInitialAudioWindowsRetainShortAndFullOffsetSearches(t *testing.T) {
	got := initialAudioWindows(155)
	want := []float64{65, 95, 155}
	if len(got) != len(want) {
		t.Fatalf("initial windows = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("initial windows = %#v, want %#v", got, want)
		}
	}
	if got = initialAudioWindows(60); len(got) != 1 || got[0] != 60 {
		t.Fatalf("bounded initial windows = %#v, want [60]", got)
	}
}
