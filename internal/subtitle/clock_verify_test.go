package subtitle

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func activityClockFixture() []Cue {
	rng := rand.New(rand.NewSource(719))
	var cues []Cue
	for start := 10.0; start < 2400; {
		duration := .7 + rng.Float64()*3
		cues = append(cues, Cue{Start: time.Duration(start * 1e9), End: time.Duration((start + duration) * 1e9), Text: []string{"dialogue"}})
		start += duration + .3 + rng.Float64()*5
	}
	return cues
}

func TestVerifyClockActivityRequiresDistributedUnambiguousEvidence(t *testing.T) {
	reference := activityClockFixture()
	for _, scenario := range []string{"correct", "small-jitter", "shift", "fps", "late-edit", "unrelated", "continuous"} {
		t.Run(scenario, func(t *testing.T) {
			target := append([]Cue(nil), reference...)
			for i := range target {
				switch scenario {
				case "small-jitter":
					target[i].Start += 40 * time.Millisecond
					target[i].End += 60 * time.Millisecond
				case "shift":
					target[i].Start += time.Second
					target[i].End += time.Second
				case "fps":
					target[i].Start = time.Duration(float64(target[i].Start) * 25 / 24)
					target[i].End = time.Duration(float64(target[i].End) * 25 / 24)
				case "late-edit":
					if i > len(target)*3/4 {
						target[i].Start += 3 * time.Second
						target[i].End += 3 * time.Second
					}
				case "unrelated":
					target[i].Start += time.Duration((i*13)%17) * time.Second
					target[i].End += time.Duration((i*13)%17) * time.Second
				case "continuous":
					target[i].Start = time.Duration(i) * 5 * time.Second
					target[i].End = target[i].Start + 5*time.Second
				}
			}
			ref := reference
			if scenario == "continuous" {
				ref = target
			}
			for variant := 0; variant < 2; variant++ {
				_, err := VerifyClockActivity(ref, target, variant)
				wantPass := scenario == "correct" || scenario == "small-jitter"
				if (err == nil) != wantPass {
					t.Fatalf("variant %d: %v", variant, err)
				}
			}
		})
	}
}

// Optional replay uses local user media, never downloads or uploads anything.
func TestVerifyClockActivityRealCompanion(t *testing.T) {
	root := os.Getenv("REDSYNC_REPLAY_DIR")
	if root == "" {
		t.Skip("set REDSYNC_REPLAY_DIR to local replay fixtures")
	}
	ref, err := Read(context.Background(), filepath.Join(root, "real-reference.vtt"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := Read(context.Background(), filepath.Join(root, "diagnostic-german-clock.vtt"))
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 2; v++ {
		correction, err := MeasureClockResidual(ref, target)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("measured correction %+v", correction)
		a, err := VerifyClockActivity(ref, Apply(target, correction), v)
		t.Logf("variant %d: %+v / %v", v, a, err)
		if err != nil {
			t.Fail()
		}
	}
}
