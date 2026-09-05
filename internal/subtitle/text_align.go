package subtitle

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/720pixel/RedSync/internal/timeline"
)

// alignTextClock measures clocks from unique shared dialogue, independent of
// cue durations and FPS metadata. Half the anchors are withheld from fitting.
// Edits, partial episodes, repeated dialogue, and inconsistent author timing
// fall through to the normal activity/piecewise/semantic recovery pipeline.
func alignTextClock(reference, target []Cue, opts AlignOptions) (Alignment, bool) {
	unique := func(cues []Cue) map[string]int {
		out := make(map[string]int)
		for i, cue := range cues {
			text := normalizeSemanticText(strings.Join(cue.Text, " "))
			if utf8.RuneCountInString(text) < 16 {
				continue
			}
			if _, exists := out[text]; exists {
				out[text] = -1
			} else {
				out[text] = i
			}
		}
		return out
	}
	refs, targets := unique(reference), unique(target)
	var anchors []timeline.Anchor
	for text, ti := range targets {
		ri, ok := refs[text]
		if !ok || ri < 0 || ti < 0 {
			continue
		}
		t := target[ti].Start.Seconds()
		anchors = append(anchors, timeline.Anchor{TargetSeconds: t, DelaySeconds: reference[ri].Start.Seconds() - t, Score: 1})
	}
	if len(anchors) < 24 {
		return Alignment{}, false
	}
	sort.Slice(anchors, func(i, j int) bool { return anchors[i].TargetSeconds < anchors[j].TargetSeconds })
	// Bound the quadratic robust fit, retaining evenly distributed witnesses.
	if len(anchors) > 256 {
		sampled := make([]timeline.Anchor, 256)
		for i := range sampled {
			sampled[i] = anchors[i*(len(anchors)-1)/(len(sampled)-1)]
		}
		anchors = sampled
	}
	refFirst, refLast := cueBounds(reference)
	targetFirst, targetLast := cueBounds(target)
	covered := func(first, last float64, times []float64) bool {
		span := last - first
		if span <= 0 || times[0]-first > math.Min(90, span*.10) || last-times[len(times)-1] > math.Min(90, span*.10) {
			return false
		}
		for i := 1; i < len(times); i++ {
			if times[i] <= times[i-1] || times[i]-times[i-1] > math.Min(180, span*.20) {
				return false
			}
		}
		return true
	}
	var fitAnchors, heldOut []timeline.Anchor
	for i, anchor := range anchors {
		if i%2 == 0 {
			fitAnchors = append(fitAnchors, anchor)
		} else {
			heldOut = append(heldOut, anchor)
		}
	}
	// Each sample must independently span both programmes.
	for _, sample := range [][]timeline.Anchor{fitAnchors, heldOut} {
		var targetTimes, refTimes []float64
		for _, anchor := range sample {
			targetTimes = append(targetTimes, anchor.TargetSeconds)
			refTimes = append(refTimes, anchor.TargetSeconds+anchor.DelaySeconds)
		}
		if !covered(targetFirst, targetLast, targetTimes) || !covered(refFirst, refLast, refTimes) {
			return Alignment{}, false
		}
	}
	fit := timeline.Piecewise(fitAnchors, targetLast, timeline.Options{MaxSegments: 1, MinAnchors: 3})
	maxOffset := opts.MaxOffsetSeconds
	if maxOffset <= 0 {
		maxOffset = 300
	}
	if math.IsNaN(fit.Scale) || fit.Scale < .8 || fit.Scale > 1.2 || math.Abs(float64(fit.OffsetMS)/1000) > maxOffset || fit.ResidualMS > 80 {
		return Alignment{}, false
	}
	// Test every anchor, not only a median that could hide an unmatched edit.
	for _, anchor := range anchors {
		if semanticFitResidualMS(fit, anchor) > 120 {
			return Alignment{}, false
		}
	}
	return Alignment{
		Method: "text-anchors", Scale: fit.Scale, OffsetMS: fit.OffsetMS,
		Score: 1, OriginalScore: dice(cueIntervals(reference, 1, 0), cueIntervals(target, 1, 0)),
		Samples: len(anchors), ResidualMS: fit.ResidualMS,
		ReferenceCues: len(reference), TargetCues: len(target),
		Segments: fit.Segments, Gaps: fit.Gaps, PreserveTargetCues: true,
	}, true
}
