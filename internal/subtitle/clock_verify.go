package subtitle

import (
	"fmt"
	"math"

	"github.com/720pixel/RedSync/internal/timeline"
)

// VerifyClockActivity corroborates an already audio-verified source clock.
// It does not fit or change that clock. Independent staggered windows must
// prefer near-zero dialogue activity over several deliberately wrong offsets;
// dense continuous captions and short/clustered matches cannot pass by chance.
func VerifyClockActivity(reference, target []Cue, variant int) (Alignment, error) {
	return measureClockActivity(reference, target, variant, false)
}

// MeasureClockResidual proposes only a small affine correction to an audio
// clock. The rendered result still needs VerifyClockActivity on other windows.
func MeasureClockResidual(reference, target []Cue) (Alignment, error) {
	return measureClockActivity(reference, target, 0, true)
}

func measureClockActivity(reference, target []Cue, variant int, correction bool) (Alignment, error) {
	maxLocal, maxOffset, maxPPM := .45, 120.0, 250.0
	if correction {
		maxLocal, maxOffset, maxPPM = 1.8, 1500, 1000
	}
	if len(reference) < 24 || len(target) < 24 {
		return Alignment{}, fmt.Errorf("source clock verification needs distributed full subtitles")
	}
	first, last := cueBounds(target)
	span := last - first
	if span < 180 {
		return Alignment{}, fmt.Errorf("source clock verification needs at least three minutes of dialogue")
	}
	count := int(math.Ceil(span / 120))
	count = max(8, min(90, count))
	var anchors []timeline.Anchor
	var rejected []string
	firstAccepted, lastAccepted := -1, -1
	for i := 0; i < count; i++ {
		fraction := (float64(i) + .30 + .4*float64(variant%2)) / float64(count)
		center := first + span*fraction
		halfWindow := math.Min(55, span/float64(count)*.48)
		lo, hi := math.Max(first, center-halfWindow), math.Min(last, center+halfWindow)
		refWindow := cuesByMidpoint(reference, lo, hi)
		targetWindow := cuesByMidpoint(target, lo, hi)
		if len(refWindow) < 4 || len(targetWindow) < 4 {
			rejected = append(rejected, fmt.Sprintf("%d:sparse(%d/%d)", i, len(refWindow), len(targetWindow)))
			continue
		}
		delay, score, ok := localSubtitleOffset(reference, target, 1, 0, center, halfWindow, 2)
		if !ok || score < .65 || math.Abs(delay) > maxLocal {
			rejected = append(rejected, fmt.Sprintf("%d:peak(%.3f/%.3fs)", i, score, delay))
			continue
		}
		refIntervals := cueIntervals(refWindow, 1, 0)
		wrong := 0.0
		for _, shift := range []float64{-12, -6, -3, 3, 6, 12} {
			wrong = math.Max(wrong, dice(refIntervals, cueIntervals(targetWindow, 1, shift)))
		}
		if score-wrong < .04 {
			rejected = append(rejected, fmt.Sprintf("%d:ambiguous(%.3f/%.3f)", i, score, wrong))
			continue
		}
		anchors = append(anchors, timeline.Anchor{TargetSeconds: center, DelaySeconds: delay, Score: score})
		if firstAccepted < 0 {
			firstAccepted = i
		}
		lastAccepted = i
	}
	if len(anchors) < 8 || len(anchors)*5 < count*4 || firstAccepted > 1 || lastAccepted < count-2 {
		return Alignment{}, fmt.Errorf("source clock activity confidence covers %d/%d independent windows; rejected %v", len(anchors), count, rejected)
	}
	for i := 1; i < len(anchors); i++ {
		if anchors[i].TargetSeconds-anchors[i-1].TargetSeconds > math.Max(180, span/float64(count)*2.1) {
			return Alignment{}, fmt.Errorf("source clock activity leaves an unverified internal region")
		}
	}
	fit := timeline.Piecewise(anchors, last, timeline.Options{MaxSegments: 1, MinAnchors: 3})
	if math.Abs(float64(fit.OffsetMS)) > maxOffset || math.Abs((fit.Scale-1)*1e6) > maxPPM || fit.ResidualMS > 200 {
		return Alignment{}, fmt.Errorf("source clock residual is not bounded (%dms, %.0fppm, %dms residual)", fit.OffsetMS, (fit.Scale-1)*1e6, fit.ResidualMS)
	}
	return Alignment{Method: "source-clock-activity", OffsetMS: fit.OffsetMS, Scale: fit.Scale, Score: fit.Score, Samples: len(anchors), ResidualMS: fit.ResidualMS}, nil
}
