package timeline

import (
	"math"
	"testing"
)

func TestPiecewiseFindsInsertAndRemoval(t *testing.T) {
	var anchors []Anchor
	// Same 25 -> 23.976-style clock throughout, with a +4s reference-only
	// section followed later by a 2.5s target-only section.
	slope := 25/(24000.0/1001) - 1
	for i := 0; i < 18; i++ {
		x := float64(30 + i*60)
		offset := -1.2
		if i >= 6 {
			offset += 4
		}
		if i >= 13 {
			offset -= 2.5
		}
		noise := float64((i%3)-1) * 0.006
		anchors = append(anchors, Anchor{TargetSeconds: x, DelaySeconds: slope*x + offset + noise, Score: 12})
	}
	fit := Piecewise(anchors, 1100, Options{MinJumpSeconds: .3})
	if len(fit.Segments) != 3 || len(fit.Gaps) != 2 {
		t.Fatalf("segments=%d gaps=%d: %#v", len(fit.Segments), len(fit.Gaps), fit)
	}
	if math.Abs(fit.Scale-(1+slope)) > .0001 {
		t.Fatalf("scale %.9f, want %.9f", fit.Scale, 1+slope)
	}
	if fit.Gaps[0].Action != "insert_silence" || math.Abs(float64(fit.Gaps[0].DeltaMS-4000)) > 20 {
		t.Fatalf("first gap = %#v", fit.Gaps[0])
	}
	if fit.Gaps[1].Action != "remove_target" || math.Abs(float64(fit.Gaps[1].DeltaMS+2500)) > 20 {
		t.Fatalf("second gap = %#v", fit.Gaps[1])
	}
}

func TestPiecewiseRejectsSingleBadAnchor(t *testing.T) {
	var anchors []Anchor
	for i := 0; i < 15; i++ {
		d := 1.25
		if i == 8 {
			d = 9
		}
		anchors = append(anchors, Anchor{TargetSeconds: float64(i * 60), DelaySeconds: d, Score: 8})
	}
	fit := Piecewise(anchors, 900, Options{})
	if len(fit.Segments) != 1 || len(fit.Gaps) != 0 {
		t.Fatalf("bad anchor became a gap: %#v", fit)
	}
}
