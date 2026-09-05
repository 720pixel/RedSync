package sync

import (
	"context"
	"testing"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/offset"
)

func TestAudioCoverageRejectsClusteredIntroMatches(t *testing.T) {
	clustered := []audioAnchor{{x: 10}, {x: 20}, {x: 30}, {x: 40}, {x: 50}}
	if missing := missingAudioRegions(clustered, 1200); len(missing) != 3 {
		t.Fatalf("clustered intro accepted: %v", missing)
	}
	distributed := []audioAnchor{{x: 100}, {x: 400}, {x: 700}, {x: 1000}}
	if missing := missingAudioRegions(distributed, 1200); len(missing) != 0 {
		t.Fatalf("distributed anchors rejected: %v", missing)
	}
}

func TestAdaptiveAudioRecoveryRequiresTwoWindowAgreement(t *testing.T) {
	decode, find := offsetDecode, offsetFindScaled
	defer func() { offsetDecode, offsetFindScaled = decode, find }()
	offsetDecode = func(_ context.Context, _ string, _ int, _, duration float64) ([]float64, error) {
		return []float64{duration}, nil
	}
	ref := media.File{Path: "ref", Duration: 1200}
	target := media.File{Path: "target", Duration: 1200}
	for _, disagreement := range []bool{false, true} {
		offsetFindScaled = func(a, b []float64, scale float64) (offset.Result, error) {
			delay := .025
			if disagreement && a[0] > 30 {
				delay = 1.25
			}
			return offset.Result{Offset: delay, Score: 8}, nil
		}
		_, ok := recoverAudioRegion(context.Background(), ref, target, 0, 0, 500, 1, 0, 4)
		if ok == disagreement {
			t.Fatalf("disagreement=%v, accepted=%v", disagreement, ok)
		}
	}
}
