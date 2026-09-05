package subtitle

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func textClockFixture(scale, shift float64) ([]Cue, []Cue) {
	var reference, target []Cue
	for i := 0; i < 120; i++ {
		start := 40 + float64(i)*20 + float64(i%7)*.13
		text := fmt.Sprintf("The distinctive dialogue at checkpoint %d continues here.", i)
		target = append(target, Cue{Start: time.Duration(start * 1e9), End: time.Duration((start + 2) * 1e9), Text: []string{"<i>" + text + "</i>"}})
		mapped := start*scale + shift
		// Different display duration must not bias the measured clock.
		reference = append(reference, Cue{Start: time.Duration(mapped * 1e9), End: time.Duration((mapped + 3.1) * 1e9), Text: []string{text}})
	}
	return reference, target
}

func TestTextClockFitsFPSAndArbitraryDriftWithIndependentAnchors(t *testing.T) {
	for _, scale := range []float64{1, 25 / (24000.0 / 1001), (24000.0 / 1001) / 25, 25.0 / 24, 24.0 / 25, 1.001, 1.017321} {
		for _, shift := range []float64{-12.345, 18.731} {
			t.Run(fmt.Sprintf("%.8f/%+.3f", scale, shift), func(t *testing.T) {
				reference, target := textClockFixture(scale, shift)
				a, err := Align(reference, target, AlignOptions{})
				if err != nil || a.Method != "text-anchors" || math.Abs(a.Scale-scale) > 1e-6 || math.Abs(float64(a.OffsetMS)-shift*1000) > 1 {
					t.Fatalf("alignment = %+v, err=%v", a, err)
				}
				finished := Apply(target, a)
				verification, ok := alignTextClock(reference, finished, AlignOptions{})
				if !ok || math.Abs(verification.Scale-1) > 1e-6 || absDuration(time.Duration(verification.OffsetMS)*time.Millisecond) > time.Millisecond || len(finished) != len(target) {
					t.Fatalf("rendered verification = %+v, ok=%v", verification, ok)
				}
			})
		}
	}
}

func TestTextClockRejectsPartialRepeatedAndEditedProgrammes(t *testing.T) {
	for _, scenario := range []string{"partial", "repeated", "late-edit", "held-out-error", "large-offset", "reordered-dialogue"} {
		t.Run(scenario, func(t *testing.T) {
			reference, target := textClockFixture(25.0/24, 2)
			switch scenario {
			case "partial":
				for i := 35; i < len(reference); i++ {
					reference[i].Text = []string{"A different episode with unrelated dialogue."}
				}
			case "repeated":
				for i := range reference {
					reference[i].Text = []string{"The repeated line is not a unique anchor."}
					target[i].Text = reference[i].Text
				}
			case "late-edit":
				for i := 90; i < len(reference); i++ {
					reference[i].Start += 7 * time.Second
					reference[i].End += 7 * time.Second
				}
			case "held-out-error":
				reference[61].Start += time.Second
			case "large-offset":
				for i := range reference {
					reference[i].Start += 400 * time.Second
					reference[i].End += 400 * time.Second
				}
			case "reordered-dialogue":
				reference[50].Text, reference[70].Text = reference[70].Text, reference[50].Text
			}
			if a, ok := alignTextClock(reference, target, AlignOptions{}); ok {
				t.Fatalf("unsafe clock accepted: %+v", a)
			}
		})
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
