// Package timeline fits a deterministic piecewise-affine map between two
// media timelines. It deliberately knows nothing about audio or subtitles;
// callers provide confidence-gated anchors obtained from their own matcher.
package timeline

import (
	"math"
	"sort"
)

// Anchor says that media at TargetSeconds belongs at TargetSeconds plus
// DelaySeconds on the reference timeline.
type Anchor struct {
	TargetSeconds float64
	DelaySeconds  float64
	Score         float64
}

// Segment is one continuous portion of the target-to-reference map.
// Reference = Target*Scale + OffsetMS/1000 within the target bounds.
type Segment struct {
	TargetStartMS    int     `json:"target_start_ms"`
	TargetEndMS      int     `json:"target_end_ms"`
	ReferenceStartMS int     `json:"reference_start_ms"`
	ReferenceEndMS   int     `json:"reference_end_ms"`
	OffsetMS         int     `json:"offset_ms"`
	Scale            float64 `json:"scale"`
	Score            float64 `json:"score,omitempty"`
	Samples          int     `json:"samples"`
	ResidualMS       int     `json:"residual_ms"`
}

// Gap describes a discontinuity between two adjacent segments. A positive
// delta is material present only in the reference and is rendered as silence.
// A negative delta is material present only in the target and is removed.
type Gap struct {
	TargetAtMS        int    `json:"target_at_ms"`
	ReferenceBeforeMS int    `json:"reference_before_ms"`
	ReferenceAfterMS  int    `json:"reference_after_ms"`
	DeltaMS           int    `json:"delta_ms"`
	DurationMS        int    `json:"duration_ms"`
	Action            string `json:"action"`
}

type Options struct {
	MinJumpSeconds float64
	MaxSegments    int
	MinAnchors     int
}

type Fit struct {
	Scale      float64
	OffsetMS   int
	Score      float64
	Samples    int
	ResidualMS int
	Segments   []Segment
	Gaps       []Gap
}

// Piecewise finds stable changes in offset while keeping one clock/FPS scale
// across the whole programme. A median adjacent-anchor slope is insensitive to
// the few large jumps caused by edits; recursive median partitions then locate
// persistent discontinuities without treating a single bad probe as a gap.
func Piecewise(in []Anchor, durationSeconds float64, opts Options) Fit {
	if len(in) == 0 {
		return Fit{Scale: 1}
	}
	anchors := append([]Anchor(nil), in...)
	sort.Slice(anchors, func(i, j int) bool { return anchors[i].TargetSeconds < anchors[j].TargetSeconds })
	if durationSeconds <= 0 {
		durationSeconds = anchors[len(anchors)-1].TargetSeconds
	}
	minJump := opts.MinJumpSeconds
	if minJump <= 0 {
		minJump = 0.35
	}
	maxSegments := opts.MaxSegments
	if maxSegments <= 0 {
		maxSegments = 8
	}
	minAnchors := opts.MinAnchors
	if minAnchors <= 0 {
		minAnchors = 2
	}

	var adjacentSlopes []float64
	for i := 1; i < len(anchors); i++ {
		dx := anchors[i].TargetSeconds - anchors[i-1].TargetSeconds
		if dx <= 0 {
			continue
		}
		s := (anchors[i].DelaySeconds - anchors[i-1].DelaySeconds) / dx
		// Valid media clock ratios are far inside this range. Larger values are
		// edits or failed probes and must not influence the common clock fit.
		if math.Abs(s) <= 0.15 {
			adjacentSlopes = append(adjacentSlopes, s)
		}
	}
	slope := median(adjacentSlopes)
	if math.Abs(slope) < 0.00002 {
		slope = 0
	}
	residualOffsets := make([]float64, len(anchors))
	for i, a := range anchors {
		residualOffsets[i] = a.DelaySeconds - slope*a.TargetSeconds
	}

	type span struct{ lo, hi int }
	spans := []span{{0, len(anchors)}}
	for len(spans) < maxSegments {
		bestSpan, bestAt := -1, -1
		bestJump := minJump
		for si, s := range spans {
			if s.hi-s.lo < minAnchors*2 {
				continue
			}
			for at := s.lo + minAnchors; at <= s.hi-minAnchors; at++ {
				left := median(residualOffsets[s.lo:at])
				right := median(residualOffsets[at:s.hi])
				jump := math.Abs(right - left)
				if jump <= bestJump {
					continue
				}
				// Both sides must be internally tighter than the proposed jump.
				// This prevents a noisy region or one bad anchor becoming an edit.
				leftMAD := medianAbsFrom(residualOffsets[s.lo:at], left)
				rightMAD := medianAbsFrom(residualOffsets[at:s.hi], right)
				if math.Max(leftMAD, rightMAD) > jump*0.40 {
					continue
				}
				bestSpan, bestAt, bestJump = si, at, jump
			}
		}
		if bestSpan < 0 {
			break
		}
		s := spans[bestSpan]
		replacement := []span{{s.lo, bestAt}, {bestAt, s.hi}}
		spans = append(spans[:bestSpan], append(replacement, spans[bestSpan+1:]...)...)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].lo < spans[j].lo })
	// Greedy partitioning may temporarily split one stable plateau in order to
	// expose a later, larger change. Collapse neighbours that do not themselves
	// differ by the requested discontinuity threshold.
	for i := 1; i < len(spans); {
		left := median(residualOffsets[spans[i-1].lo:spans[i-1].hi])
		right := median(residualOffsets[spans[i].lo:spans[i].hi])
		if math.Abs(right-left) < minJump {
			spans[i-1].hi = spans[i].hi
			spans = append(spans[:i], spans[i+1:]...)
			continue
		}
		i++
	}

	// Once edits are separated, a Theil-Sen fit using every within-segment pair
	// gives a more precise clock ratio than short adjacent differences.
	var cleanSlopes []float64
	for _, s := range spans {
		for i := s.lo; i < s.hi; i++ {
			for j := i + 1; j < s.hi; j++ {
				dx := anchors[j].TargetSeconds - anchors[i].TargetSeconds
				if dx > 0 {
					cleanSlopes = append(cleanSlopes, (anchors[j].DelaySeconds-anchors[i].DelaySeconds)/dx)
				}
			}
		}
	}
	if len(cleanSlopes) > 0 {
		slope = median(cleanSlopes)
		if math.Abs(slope) < 0.00002 {
			slope = 0
		}
		for i, a := range anchors {
			residualOffsets[i] = a.DelaySeconds - slope*a.TargetSeconds
		}
	}

	scale := 1 + slope
	segments := make([]Segment, 0, len(spans))
	allResiduals := make([]float64, 0, len(anchors))
	allScores := make([]float64, 0, len(anchors))
	for i, s := range spans {
		start := 0.0
		if i > 0 {
			start = (anchors[s.lo-1].TargetSeconds + anchors[s.lo].TargetSeconds) / 2
		}
		end := durationSeconds
		if i+1 < len(spans) {
			at := spans[i+1].lo
			end = (anchors[at-1].TargetSeconds + anchors[at].TargetSeconds) / 2
		}
		offset := median(residualOffsets[s.lo:s.hi])
		res := make([]float64, 0, s.hi-s.lo)
		scores := make([]float64, 0, s.hi-s.lo)
		for ai := s.lo; ai < s.hi; ai++ {
			res = append(res, math.Abs(residualOffsets[ai]-offset))
			if anchors[ai].Score > 0 {
				scores = append(scores, anchors[ai].Score)
				allScores = append(allScores, anchors[ai].Score)
			}
			allResiduals = append(allResiduals, math.Abs(residualOffsets[ai]-offset))
		}
		segments = append(segments, newSegment(start, end, scale, offset, median(scores), len(res), median(res)))
	}

	gaps := make([]Gap, 0, len(segments)-1)
	for i := 1; i < len(segments); i++ {
		at := segments[i].TargetStartMS
		before := mapMS(segments[i-1], at)
		after := mapMS(segments[i], at)
		delta := after - before
		action := "insert_silence"
		if delta < 0 {
			action = "remove_target"
		}
		gaps = append(gaps, Gap{
			TargetAtMS: at, ReferenceBeforeMS: before, ReferenceAfterMS: after,
			DeltaMS: delta, DurationMS: absInt(delta), Action: action,
		})
	}
	// A negative offset jump represents target-only material. Express that
	// section directly as a hole between source segments: TargetAtMS is the cut
	// start and the following segment begins after the removed duration.
	for i := range gaps {
		if gaps[i].DeltaMS >= 0 {
			continue
		}
		at := gaps[i].TargetAtMS
		scale := segments[i+1].Scale
		if scale <= 0 {
			scale = 1
		}
		resume := at + int(math.Round(float64(gaps[i].DurationMS)/scale))
		segments[i].TargetEndMS = at
		segments[i].ReferenceEndMS = mapMS(segments[i], at)
		segments[i+1].TargetStartMS = resume
		segments[i+1].ReferenceStartMS = mapMS(segments[i+1], resume)
		gaps[i].ReferenceBeforeMS = segments[i].ReferenceEndMS
		gaps[i].ReferenceAfterMS = segments[i+1].ReferenceStartMS
	}

	return Fit{
		Scale: scale, OffsetMS: segments[0].OffsetMS, Score: median(allScores),
		Samples: len(anchors), ResidualMS: int(math.Round(median(allResiduals) * 1000)),
		Segments: segments, Gaps: gaps,
	}
}

// SetBoundary moves a detected change point after media-specific refinement.
func SetBoundary(f *Fit, gapIndex int, targetSeconds float64) {
	if f == nil || gapIndex < 0 || gapIndex >= len(f.Gaps) || gapIndex+1 >= len(f.Segments) {
		return
	}
	at := int(math.Round(targetSeconds * 1000))
	f.Segments[gapIndex].TargetEndMS = at
	f.Segments[gapIndex].ReferenceEndMS = mapMS(f.Segments[gapIndex], at)
	nextStart := at
	oldGap := f.Gaps[gapIndex]
	if oldGap.DeltaMS < 0 {
		scale := f.Segments[gapIndex+1].Scale
		if scale <= 0 {
			scale = 1
		}
		nextStart += int(math.Round(float64(oldGap.DurationMS) / scale))
	}
	f.Segments[gapIndex+1].TargetStartMS = nextStart
	f.Segments[gapIndex+1].ReferenceStartMS = mapMS(f.Segments[gapIndex+1], nextStart)
	before := f.Segments[gapIndex].ReferenceEndMS
	after := f.Segments[gapIndex+1].ReferenceStartMS
	if oldGap.DeltaMS < 0 {
		f.Gaps[gapIndex] = Gap{
			TargetAtMS: at, ReferenceBeforeMS: before, ReferenceAfterMS: after,
			DeltaMS: oldGap.DeltaMS, DurationMS: oldGap.DurationMS, Action: "remove_target",
		}
		return
	}
	delta := after - before
	f.Gaps[gapIndex] = Gap{
		TargetAtMS: at, ReferenceBeforeMS: before, ReferenceAfterMS: after,
		DeltaMS: delta, DurationMS: absInt(delta), Action: "insert_silence",
	}
}

func newSegment(start, end, scale, offset, score float64, samples int, residual float64) Segment {
	s := Segment{
		TargetStartMS: int(math.Round(start * 1000)), TargetEndMS: int(math.Round(end * 1000)),
		OffsetMS: int(math.Round(offset * 1000)), Scale: scale, Score: score,
		Samples: samples, ResidualMS: int(math.Round(residual * 1000)),
	}
	s.ReferenceStartMS = mapMS(s, s.TargetStartMS)
	s.ReferenceEndMS = mapMS(s, s.TargetEndMS)
	return s
}

func mapMS(s Segment, targetMS int) int {
	return int(math.Round(float64(targetMS)*s.Scale)) + s.OffsetMS
}

func medianAbsFrom(v []float64, center float64) float64 {
	d := make([]float64, len(v))
	for i, x := range v {
		d[i] = math.Abs(x - center)
	}
	return median(d)
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	m := len(c) / 2
	if len(c)%2 == 0 {
		return (c[m-1] + c[m]) / 2
	}
	return c[m]
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
