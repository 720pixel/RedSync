package sync

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/720pixel/RedSync/internal/media"
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
