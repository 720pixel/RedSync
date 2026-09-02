package subtitle

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/720pixel/RedSync/internal/timeline"
	"gonum.org/v1/gonum/dsp/fourier"
)

// Alignment is the affine map from target subtitle time to reference time:
// reference = target*Scale + OffsetMS. It handles fixed delays, arbitrary
// duration drift, and the usual film/TV frame-rate conversions.
type Alignment struct {
	Method             string
	OffsetMS           int
	Scale              float64
	Score              float64
	OriginalScore      float64
	ReferenceCues      int
	TargetCues         int
	Samples            int
	ResidualMS         int
	Segments           []timeline.Segment
	Gaps               []timeline.Gap
	PreserveTargetCues bool
}

type AlignOptions struct {
	MaxOffsetSeconds float64
	MinScore         float64
	MinGapSeconds    float64
	MaxSegments      int
	DisablePiecewise bool
}

type interval struct{ start, end float64 }

// Align compares language-independent cue activity. A coarse FFT search finds
// the dominant offset for many possible speed ratios; an exact interval-overlap
// refinement then resolves the result to milliseconds without quantizing cues.
func Align(reference, target []Cue, opts AlignOptions) (Alignment, error) {
	if len(reference) < 3 || len(target) < 3 {
		return Alignment{}, fmt.Errorf("need at least 3 cues in both reference and target")
	}
	maxOffset := opts.MaxOffsetSeconds
	if maxOffset <= 0 {
		maxOffset = 300
	}
	minScore := opts.MinScore
	if minScore <= 0 {
		minScore = 0.10
	}
	refIntervals := cueIntervals(reference, 1, 0)
	targetIntervals := cueIntervals(target, 1, 0)
	original := dice(refIntervals, targetIntervals)

	refFirst, refLast := cueBounds(reference)
	targetFirst, targetLast := cueBounds(target)
	durationRatio := (refLast - refFirst) / (targetLast - targetFirst)
	candidates := []float64{
		1, durationRatio,
		25 / (24000.0 / 1001), (24000.0 / 1001) / 25,
		25.0 / 24, 24.0 / 25,
		24 / (24000.0 / 1001), (24000.0 / 1001) / 24,
	}
	for s := 0.94; s <= 1.060001; s += 0.0025 {
		candidates = append(candidates, s)
	}
	candidates = uniqueScales(candidates)

	bestScale, bestOffset, bestScore := 1.0, 0.0, -1.0
	for _, scale := range candidates {
		if scale < 0.8 || scale > 1.2 {
			continue
		}
		offset := fftOffset(reference, target, scale, 0.10, maxOffset)
		score := dice(refIntervals, cueIntervals(target, scale, offset))
		if score > bestScore {
			bestScale, bestOffset, bestScore = scale, offset, score
		}
	}

	// Changing scale rotates the timeline around its beginning. Recenter each
	// fine candidate near the midpoint so its expected offset stays close, then
	// search a small 5ms grid using exact interval intersections.
	center := (targetFirst + targetLast) / 2
	coarseScale, coarseOffset := bestScale, bestOffset
	for scale := coarseScale - 0.003; scale <= coarseScale+0.0030001; scale += 0.00005 {
		if scale < 0.8 || scale > 1.2 {
			continue
		}
		expectedOffset := coarseOffset + (coarseScale-scale)*center
		for off := expectedOffset - 0.30; off <= expectedOffset+0.3001; off += 0.005 {
			if math.Abs(off) > maxOffset {
				continue
			}
			score := dice(refIntervals, cueIntervals(target, scale, off))
			if score > bestScore {
				bestScale, bestOffset, bestScore = scale, off, score
			}
		}
	}

	if bestScore < minScore {
		return Alignment{}, fmt.Errorf("subtitle match confidence is too low (%.3f < %.3f); files may not describe the same programme", bestScore, minScore)
	}
	// The fine search can land a few ppm away from 1 on flat score plateaus. Do
	// a dedicated millisecond offset refinement at exactly 1 before accepting a
	// tiny, unproven speed change.
	if math.Abs(bestScale-1) < 0.00015 {
		oneOffset := bestOffset + (bestScale-1)*center
		oneScore, oneBest := -1.0, oneOffset
		for off := oneOffset - 0.20; off <= oneOffset+0.2001; off += 0.001 {
			score := dice(refIntervals, cueIntervals(target, 1, off))
			if score > oneScore {
				oneScore, oneBest = score, off
			}
		}
		if oneScore >= bestScore-0.002 {
			bestScale, bestOffset, bestScore = 1, oneBest, oneScore
		}
	}
	if bestScore <= original+0.001 {
		bestScale, bestOffset, bestScore = 1, 0, original
	}
	global := Alignment{
		Method:        "activity",
		OffsetMS:      int(math.Round(bestOffset * 1000)),
		Scale:         bestScale,
		Score:         bestScore,
		OriginalScore: original,
		ReferenceCues: len(reference),
		TargetCues:    len(target),
	}
	return piecewiseSubtitleAlignment(reference, target, global, opts), nil
}

// Apply retimes every cue and drops events that land wholly before zero.
func Apply(cues []Cue, a Alignment) []Cue {
	if len(a.Segments) > 1 {
		return applyPiecewise(cues, a)
	}
	scale := a.Scale
	if scale <= 0 {
		scale = 1
	}
	offset := time.Duration(a.OffsetMS) * time.Millisecond
	out := make([]Cue, 0, len(cues))
	for _, c := range cues {
		start := time.Duration(math.Round(float64(c.Start)*scale)) + offset
		end := time.Duration(math.Round(float64(c.End)*scale)) + offset
		if end <= 0 || end <= start {
			continue
		}
		if start < 0 {
			start = 0
		}
		c.Start, c.End = start, end
		out = append(out, c)
	}
	return out
}

func piecewiseSubtitleAlignment(reference, target []Cue, global Alignment, opts AlignOptions) Alignment {
	targetFirst, targetLast := cueBounds(target)
	duration := targetLast
	span := targetLast - targetFirst
	if span < 90 {
		return global
	}
	count := int(math.Ceil(span/90)) + 1
	count = max(7, min(19, count))
	halfWindow := math.Max(45, math.Min(120, span/8))
	search := math.Min(45, opts.MaxOffsetSeconds)
	if search <= 0 {
		search = 45
	}
	expectedOffset := float64(global.OffsetMS) / 1000
	minLocalScore := math.Max(0.06, opts.MinScore*0.60)
	if opts.MinScore <= 0 {
		minLocalScore = 0.06
	}
	var anchors []timeline.Anchor
	for i := 0; i < count; i++ {
		center := targetFirst + span*(0.04+0.92*float64(i)/float64(count-1))
		offset, score, ok := localSubtitleOffset(reference, target, global.Scale, expectedOffset, center, halfWindow, search)
		if !ok || score < minLocalScore {
			continue
		}
		anchors = append(anchors, timeline.Anchor{
			TargetSeconds: center,
			DelaySeconds:  (global.Scale-1)*center + offset,
			Score:         score,
		})
	}
	if len(anchors) < 4 {
		return global
	}
	maxSegments := opts.MaxSegments
	if opts.DisablePiecewise {
		maxSegments = 1
	}
	fit := timeline.Piecewise(anchors, duration, timeline.Options{
		MinJumpSeconds: opts.MinGapSeconds,
		MaxSegments:    maxSegments,
	})
	refineSubtitleBoundaries(reference, target, &fit)
	global.OffsetMS = fit.OffsetMS
	global.Scale = fit.Scale
	global.Samples = fit.Samples
	global.ResidualMS = fit.ResidualMS
	global.Segments = fit.Segments
	global.Gaps = fit.Gaps
	return global
}

func refineSubtitleBoundaries(reference, target []Cue, fit *timeline.Fit) {
	for i := range fit.Gaps {
		if i+1 >= len(fit.Segments) {
			break
		}
		guess := float64(fit.Gaps[i].TargetAtMS) / 1000
		gapDuration := float64(fit.Gaps[i].DurationMS) / 1000
		radius := math.Min(180, math.Max(90, gapDuration*6))
		lo := math.Max(float64(fit.Segments[i].TargetStartMS)/1000, guess-radius)
		hi := math.Min(float64(fit.Segments[i+1].TargetEndMS)/1000, guess+radius)
		window := math.Min(90, math.Max(30, (hi-lo)/3))
		bestAt := guess
		bestScore := subtitleBoundaryScore(reference, target, fit.Segments[i], fit.Segments[i+1], fit.Gaps[i], guess, window)
		for candidate := lo; candidate <= hi; candidate += .5 {
			score := subtitleBoundaryScore(reference, target, fit.Segments[i], fit.Segments[i+1], fit.Gaps[i], candidate, window)
			if score > bestScore {
				bestAt, bestScore = candidate, score
			}
		}
		coarse := bestAt
		for candidate := coarse - .75; candidate <= coarse+.7501; candidate += .025 {
			if candidate < lo || candidate > hi {
				continue
			}
			score := subtitleBoundaryScore(reference, target, fit.Segments[i], fit.Segments[i+1], fit.Gaps[i], candidate, window)
			if score > bestScore {
				bestAt, bestScore = candidate, score
			}
		}
		timeline.SetBoundary(fit, i, bestAt)
	}
}

func subtitleBoundaryScore(reference, target []Cue, before, after timeline.Segment, gap timeline.Gap, candidate, window float64) float64 {
	beforeTarget := cuesOverlapping(target, candidate-window, candidate)
	if len(beforeTarget) < 2 {
		return 0
	}
	beforeRefStart := before.Scale*(candidate-window) + float64(before.OffsetMS)/1000
	beforeRefEnd := before.Scale*candidate + float64(before.OffsetMS)/1000
	beforeReference := cuesOverlapping(reference, beforeRefStart, beforeRefEnd)
	beforeScore := dice(cueIntervals(beforeReference, 1, 0), cueIntervals(beforeTarget, before.Scale, float64(before.OffsetMS)/1000))

	afterStart := candidate
	if gap.DeltaMS < 0 {
		afterStart += float64(gap.DurationMS) / 1000 / after.Scale
	}
	afterTarget := cuesOverlapping(target, afterStart, afterStart+window)
	if len(afterTarget) < 2 {
		return 0
	}
	afterRefStart := after.Scale*afterStart + float64(after.OffsetMS)/1000
	afterRefEnd := after.Scale*(afterStart+window) + float64(after.OffsetMS)/1000
	afterReference := cuesOverlapping(reference, afterRefStart, afterRefEnd)
	afterScore := dice(cueIntervals(afterReference, 1, 0), cueIntervals(afterTarget, after.Scale, float64(after.OffsetMS)/1000))
	return (beforeScore + afterScore) / 2
}

func localSubtitleOffset(reference, target []Cue, scale, expectedOffset, center, halfWindow, search float64) (float64, float64, bool) {
	targetChunk := cuesByMidpoint(target, center-halfWindow, center+halfWindow)
	if len(targetChunk) < 5 {
		return 0, 0, false
	}
	refCenter := scale*center + expectedOffset
	refChunk := cuesOverlapping(reference, refCenter-halfWindow*scale-search, refCenter+halfWindow*scale+search)
	if len(refChunk) < 5 {
		return 0, 0, false
	}
	refIntervals := cueIntervals(refChunk, 1, 0)
	bestOffset, bestScore := expectedOffset, -1.0
	for off := expectedOffset - search; off <= expectedOffset+search+0.0001; off += 0.10 {
		score := dice(refIntervals, cueIntervals(targetChunk, scale, off))
		if score > bestScore {
			bestOffset, bestScore = off, score
		}
	}
	coarse := bestOffset
	for off := coarse - 0.15; off <= coarse+0.1501; off += 0.005 {
		score := dice(refIntervals, cueIntervals(targetChunk, scale, off))
		if score > bestScore {
			bestOffset, bestScore = off, score
		}
	}
	return bestOffset, bestScore, true
}

func cuesByMidpoint(cues []Cue, start, end float64) []Cue {
	out := make([]Cue, 0)
	for _, cue := range cues {
		mid := float64(cue.Start+cue.End) / 2 / float64(time.Second)
		if mid >= start && mid <= end {
			out = append(out, cue)
		}
	}
	return out
}

func cuesOverlapping(cues []Cue, start, end float64) []Cue {
	out := make([]Cue, 0)
	for _, cue := range cues {
		s := float64(cue.Start) / float64(time.Second)
		e := float64(cue.End) / float64(time.Second)
		if e >= start && s <= end {
			out = append(out, cue)
		}
	}
	return out
}

func applyPiecewise(cues []Cue, a Alignment) []Cue {
	type removedRange struct{ start, end float64 }
	var removed []removedRange
	for i, gap := range a.Gaps {
		if a.PreserveTargetCues {
			break
		}
		if gap.DeltaMS >= 0 || i >= len(a.Segments) {
			continue
		}
		scale := a.Segments[i].Scale
		if scale <= 0 {
			scale = 1
		}
		start := float64(gap.TargetAtMS)
		removed = append(removed, removedRange{start: start, end: start + float64(gap.DurationMS)/scale})
	}
	out := make([]Cue, 0, len(cues))
	for _, cue := range cues {
		midMS := float64((cue.Start + cue.End) / 2 / time.Millisecond)
		drop := false
		for _, r := range removed {
			if midMS >= r.start && midMS < r.end {
				drop = true
				break
			}
		}
		if drop {
			continue
		}
		segment := a.Segments[len(a.Segments)-1]
		for _, candidate := range a.Segments {
			if int(math.Round(midMS)) < candidate.TargetEndMS {
				segment = candidate
				break
			}
		}
		scale := segment.Scale
		if scale <= 0 {
			scale = 1
		}
		offset := time.Duration(segment.OffsetMS) * time.Millisecond
		start := time.Duration(math.Round(float64(cue.Start)*scale)) + offset
		end := time.Duration(math.Round(float64(cue.End)*scale)) + offset
		if end <= 0 || end <= start {
			continue
		}
		if start < 0 {
			start = 0
		}
		cue.Start, cue.End = start, end
		out = append(out, cue)
	}
	return out
}

func cueBounds(cues []Cue) (float64, float64) {
	first := float64(cues[0].Start) / float64(time.Second)
	last := float64(cues[0].End) / float64(time.Second)
	for _, c := range cues[1:] {
		first = math.Min(first, float64(c.Start)/float64(time.Second))
		last = math.Max(last, float64(c.End)/float64(time.Second))
	}
	return first, last
}

func cueIntervals(cues []Cue, scale, offset float64) []interval {
	spans := make([]interval, 0, len(cues))
	for _, c := range cues {
		s := float64(c.Start)/float64(time.Second)*scale + offset
		e := float64(c.End)/float64(time.Second)*scale + offset
		if e > s {
			spans = append(spans, interval{s, e})
		}
	}
	return mergeIntervals(spans)
}

func mergeIntervals(in []interval) []interval {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i].start < in[j].start })
	out := []interval{in[0]}
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if s.start <= last.end {
			last.end = math.Max(last.end, s.end)
		} else {
			out = append(out, s)
		}
	}
	return out
}

func dice(a, b []interval) float64 {
	var totalA, totalB, overlap float64
	for _, x := range a {
		totalA += x.end - x.start
	}
	for _, x := range b {
		totalB += x.end - x.start
	}
	for i, j := 0, 0; i < len(a) && j < len(b); {
		lo := math.Max(a[i].start, b[j].start)
		hi := math.Min(a[i].end, b[j].end)
		if hi > lo {
			overlap += hi - lo
		}
		if a[i].end < b[j].end {
			i++
		} else {
			j++
		}
	}
	if totalA+totalB == 0 {
		return 0
	}
	return 2 * overlap / (totalA + totalB)
}

func fftOffset(reference, target []Cue, scale, step, maxOffset float64) float64 {
	_, refEnd := cueBounds(reference)
	_, targetEnd := cueBounds(target)
	targetEnd *= scale
	na := int(math.Ceil(math.Max(0, refEnd)/step)) + 2
	nb := int(math.Ceil(math.Max(0, targetEnd)/step)) + 2
	full := na + nb - 1
	size := 1
	for size < full {
		size <<= 1
	}
	a := make([]float64, size)
	b := make([]float64, size)
	markActivity(a[:na], reference, 1, step)
	markActivity(b[:nb], target, scale, step)
	// Reverse target so convolution is cross-correlation.
	for i, j := 0, nb-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	fft := fourier.NewFFT(size)
	fa := fft.Coefficients(nil, a)
	fb := fft.Coefficients(nil, b)
	for i := range fa {
		fa[i] *= fb[i]
	}
	corr := fft.Sequence(nil, fa)
	best, bestLag := math.Inf(-1), 0
	maxBins := int(math.Round(maxOffset / step))
	for idx := 0; idx < full; idx++ {
		lag := idx - (nb - 1)
		if lag < -maxBins || lag > maxBins {
			continue
		}
		if corr[idx] > best {
			best, bestLag = corr[idx], lag
		}
	}
	return float64(bestLag) * step
}

func markActivity(dst []float64, cues []Cue, scale, step float64) {
	for _, c := range cues {
		start := int(math.Floor(float64(c.Start) / float64(time.Second) * scale / step))
		end := int(math.Ceil(float64(c.End) / float64(time.Second) * scale / step))
		start = max(start, 0)
		end = min(end, len(dst))
		for i := start; i < end; i++ {
			dst[i] = 1
		}
	}
}

func uniqueScales(in []float64) []float64 {
	var out []float64
	for _, v := range in {
		seen := false
		for _, old := range out {
			if math.Abs(old-v) < 0.00001 {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, v)
		}
	}
	return out
}
