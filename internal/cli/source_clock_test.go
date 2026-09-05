package cli

import (
	"context"
	"math"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/720pixel/RedSync/internal/subtitle"
	"github.com/720pixel/RedSync/internal/timeline"
)

func TestCompanionSourceClockCorrectsResidualAndVerifiesRenderedCues(t *testing.T) {
	var reference, target []subtitle.Cue
	rng := rand.New(rand.NewSource(517))
	for start := 10.0; start < 2400; {
		duration := .8 + rng.Float64()*3
		target = append(target, subtitle.Cue{Start: time.Duration(start * 1e9), End: time.Duration((start + duration) * 1e9), Text: []string{"translated dialogue"}})
		reference = append(reference, subtitle.Cue{Start: time.Duration((start*1.00012 + .2) * 1e9), End: time.Duration(((start+duration)*1.00012 + .2) * 1e9), Text: []string{"reference dialogue"}})
		start += duration + .4 + rng.Float64()*5
	}
	plan := alignmentPlan{Scale: 1, ReferenceDurationSeconds: 2500, Segments: []timeline.Segment{{TargetEndMS: 2500000, ReferenceEndMS: 2500000, Scale: 1}}}
	a, err := corroboratedSubtitleSourceClock(reference, target, plan)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(a.Scale-1.00012) > .00002 || math.Abs(float64(a.OffsetMS)-200) > 20 {
		t.Fatalf("wrong residual map: %+v", a)
	}
	expected := subtitle.Apply(target, a)
	output := filepath.Join(t.TempDir(), "finished.vtt")
	if err := subtitle.Write(context.Background(), output, expected, true); err != nil {
		t.Fatal(err)
	}
	v, err := verifyCorroboratedSubtitleClock(context.Background(), reference, expected, output)
	if err != nil || !v.Passed || v.Policy != "source-timeline-independent-activity" {
		t.Fatalf("verification: %+v %v", v, err)
	}
	tampered := append([]subtitle.Cue(nil), expected...)
	tampered[12].Text = []string{"lost dialogue"}
	if err := subtitle.Write(context.Background(), output, tampered, true); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyCorroboratedSubtitleClock(context.Background(), reference, expected, output); err == nil {
		t.Fatal("changed output was accepted")
	}
}
