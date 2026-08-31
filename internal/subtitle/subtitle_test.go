package subtitle

import (
	"fmt"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/720pixel/RedSync/internal/timeline"
)

func TestParseSRTAndVTT(t *testing.T) {
	for _, input := range []string{
		"\ufeff1\r\n00:00:06,256 --> 00:00:09,134\r\nhello\r\n\r\n",
		"WEBVTT\n\n00:06.256 --> 00:09.134 position:10%\nhello\n\n",
	} {
		cues, err := parseTimedText([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(cues) != 1 || cues[0].Start != 6256*time.Millisecond || cues[0].End != 9134*time.Millisecond {
			t.Fatalf("unexpected cues: %#v", cues)
		}
	}
}

func TestAlignDetectsTargetOnlySubtitleSection(t *testing.T) {
	var reference, target []Cue
	for i := 0; i < 180; i++ {
		start := 9 + float64(i)*5.41 + float64((i*i)%17)*0.14
		dur := .65 + float64((i*3)%11)*.09
		reference = append(reference, secondsCue(start, start+dur, i))
		targetStart := start
		if start >= 420 {
			targetStart += 4
		}
		target = append(target, secondsCue(targetStart, targetStart+dur, i))
	}
	target = append(target, secondsCue(420.4, 421.2, 1001), secondsCue(422.1, 423.3, 1002))
	sort.Slice(target, func(i, j int) bool { return target[i].Start < target[j].Start })
	a, err := Align(reference, target, AlignOptions{MaxOffsetSeconds: 30, MinGapSeconds: .35})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Gaps) != 1 || a.Gaps[0].Action != "remove_target" {
		t.Fatalf("alignment gaps: %#v", a.Gaps)
	}
	if math.Abs(float64(a.Gaps[0].DurationMS-4000)) > 150 {
		t.Fatalf("gap = %#v", a.Gaps[0])
	}
	if math.Abs(float64(a.Gaps[0].TargetAtMS)-420_000) > 750 {
		t.Fatalf("gap boundary = %dms, want 420000ms", a.Gaps[0].TargetAtMS)
	}
}

func TestAlignDetectsInternalReferenceGap(t *testing.T) {
	var reference, target []Cue
	for i := 0; i < 170; i++ {
		start := 10 + float64(i)*5.73 + float64((i*i)%13)*0.11
		dur := .7 + float64((i*5)%7)*.13
		offset := -1.5
		if start >= 420 {
			offset += 6
		}
		target = append(target, secondsCue(start, start+dur, i))
		reference = append(reference, secondsCue(start+offset, start+dur+offset, i))
	}
	a, err := Align(reference, target, AlignOptions{MaxOffsetSeconds: 30, MinGapSeconds: .35})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Segments) != 2 || len(a.Gaps) != 1 {
		t.Fatalf("segments=%d gaps=%d: %#v", len(a.Segments), len(a.Gaps), a)
	}
	if a.Gaps[0].Action != "insert_silence" || math.Abs(float64(a.Gaps[0].DeltaMS-6000)) > 120 {
		t.Fatalf("gap = %#v", a.Gaps[0])
	}
	if math.Abs(float64(a.Gaps[0].TargetAtMS)-420_000) > 500 {
		t.Fatalf("gap boundary = %dms, want 420000ms", a.Gaps[0].TargetAtMS)
	}
}

func TestApplyPiecewiseDropsTargetOnlySection(t *testing.T) {
	cues := []Cue{
		secondsCue(90, 91, 1),
		secondsCue(102, 103, 2), // target-only section
		secondsCue(110, 111, 3),
	}
	a := Alignment{
		Scale: 1,
		Segments: []timeline.Segment{
			{TargetStartMS: 0, TargetEndMS: 100_000, OffsetMS: 0, Scale: 1},
			{TargetStartMS: 105_000, TargetEndMS: 200_000, OffsetMS: -5_000, Scale: 1},
		},
		Gaps: []timeline.Gap{{TargetAtMS: 100_000, DeltaMS: -5_000, DurationMS: 5_000, Action: "remove_target"}},
	}
	got := Apply(cues, a)
	if len(got) != 2 {
		t.Fatalf("got %d cues, want 2: %#v", len(got), got)
	}
	if got[0].Start != 90*time.Second || got[1].Start != 105*time.Second {
		t.Fatalf("retimed cues = %#v", got)
	}
}

func TestAlignRecoversOffsetAndSpeed(t *testing.T) {
	var reference, target []Cue
	scale, offset := 0.96, -3.2
	for i := 0; i < 80; i++ {
		// Irregular spacing/durations avoid a periodic activity signal.
		start := 8 + float64(i)*7.31 + float64((i*i)%11)*0.23
		dur := 0.8 + float64((i*7)%9)*0.17
		reference = append(reference, secondsCue(start, start+dur, i))
		target = append(target, secondsCue((start-offset)/scale, (start+dur-offset)/scale, i))
	}
	a, err := Align(reference, target, AlignOptions{MaxOffsetSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(a.Scale-scale) > 0.00015 {
		t.Fatalf("scale = %.9f, want %.9f", a.Scale, scale)
	}
	if math.Abs(float64(a.OffsetMS)/1000-offset) > 0.03 {
		t.Fatalf("offset = %dms, want %.0fms", a.OffsetMS, offset*1000)
	}
	if a.Score < 0.98 {
		t.Fatalf("score = %.4f, want >= .98", a.Score)
	}
}

func TestApplyClampsAndDropsNegativeCues(t *testing.T) {
	cues := []Cue{
		{Start: time.Second, End: 2 * time.Second, Text: []string{"drop"}},
		{Start: 3 * time.Second, End: 5 * time.Second, Text: []string{"keep"}},
	}
	got := Apply(cues, Alignment{Scale: 1, OffsetMS: -2500})
	if len(got) != 1 || got[0].Start != 500*time.Millisecond || got[0].End != 2500*time.Millisecond {
		t.Fatalf("Apply() = %#v", got)
	}
}

func secondsCue(start, end float64, i int) Cue {
	return Cue{
		Start: time.Duration(start * float64(time.Second)),
		End:   time.Duration(end * float64(time.Second)),
		Text:  []string{fmt.Sprintf("cue %d", i)},
	}
}
