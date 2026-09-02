package subtitle

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/720pixel/RedSync/internal/timeline"
)

// Embedder maps text in different languages into one shared vector space.
// Implementations must return vectors in input order and must be safe for
// repeated calls. AlignSemantic itself never performs network access.
type Embedder interface {
	Embed(context.Context, []string) ([][]float64, error)
}

// SemanticOptions controls the strict cross-language subtitle matcher.
type SemanticOptions struct {
	AlignOptions
	MinSimilarity       float64
	MinMargin           float64
	SearchWindowSeconds float64
	MaxBlockCues        int
}

type semanticUnit struct {
	first, last int
	mid         float64
	text        string
	vector      []float64
}

type semanticMatch struct {
	target, reference semanticUnit
	similarity        float64
	margin            float64
}

// AlignSemantic aligns translated subtitles using multilingual sentence
// embeddings. It accepts only distinctive mutual matches, finds the strongest
// monotonic chain, then fits the same deterministic piecewise timing model used
// by the audio path. Sparse, clustered, ambiguous, or high-residual matches are
// rejected rather than producing a plausible-looking but unsafe shift.
func AlignSemantic(ctx context.Context, reference, target []Cue, embedder Embedder, opts SemanticOptions) (Alignment, error) {
	if embedder == nil {
		return Alignment{}, fmt.Errorf("semantic subtitle alignment requires an embedder")
	}
	if len(reference) < 6 || len(target) < 6 {
		return Alignment{}, fmt.Errorf("semantic subtitle alignment needs at least 6 cues in both files")
	}
	minSimilarity := opts.MinSimilarity
	if minSimilarity <= 0 {
		minSimilarity = 0.58
	}
	minMargin := opts.MinMargin
	if minMargin <= 0 {
		minMargin = 0.035
	}
	maxBlock := opts.MaxBlockCues
	if maxBlock <= 0 {
		maxBlock = 2
	}
	if maxBlock > 3 {
		maxBlock = 3
	}

	seed := semanticSeed(reference, target, opts.AlignOptions)
	window := opts.SearchWindowSeconds
	if window <= 0 {
		window = math.Max(120, opts.MaxOffsetSeconds)
	}
	window = math.Max(window, 30)

	refUnits := semanticUnits(reference, maxBlock)
	targetUnits := semanticUnits(target, maxBlock)
	allTexts := make([]string, 0, len(refUnits)+len(targetUnits))
	for _, unit := range refUnits {
		allTexts = append(allTexts, unit.text)
	}
	for _, unit := range targetUnits {
		allTexts = append(allTexts, unit.text)
	}
	vectors, err := embedder.Embed(ctx, allTexts)
	if err != nil {
		return Alignment{}, fmt.Errorf("semantic subtitle embeddings: %w", err)
	}
	if len(vectors) != len(allTexts) {
		return Alignment{}, fmt.Errorf("semantic embedder returned %d vectors for %d texts", len(vectors), len(allTexts))
	}
	for i := range refUnits {
		refUnits[i].vector = vectors[i]
	}
	for i := range targetUnits {
		targetUnits[i].vector = vectors[len(refUnits)+i]
	}

	matches := mutualSemanticMatches(refUnits, targetUnits, seed, window, minSimilarity, minMargin)
	matches = monotonicSemanticMatches(matches)
	minimum := max(6, min(20, min(len(reference), len(target))/20))
	if len(matches) < minimum {
		return Alignment{}, fmt.Errorf("semantic subtitle confidence is too low: %d distinctive matches, need %d", len(matches), minimum)
	}

	targetFirst, targetLast := cueBounds(target)
	coverage := semanticCoverage(matches, targetFirst, targetLast)
	if coverage < 0.45 {
		return Alignment{}, fmt.Errorf("semantic subtitle coverage is too narrow (%.0f%% < 45%%); refusing a partial or mismatched programme", coverage*100)
	}
	if buckets := semanticCoverageBuckets(matches, targetFirst, targetLast); buckets < 3 {
		return Alignment{}, fmt.Errorf("semantic subtitle anchors cover only %d/4 programme regions; refusing a clustered partial match", buckets)
	}
	similarities := make([]float64, len(matches))
	anchors := make([]timeline.Anchor, len(matches))
	for i, match := range matches {
		similarities[i] = match.similarity
		anchors[i] = timeline.Anchor{
			TargetSeconds: match.target.mid,
			DelaySeconds:  match.reference.mid - match.target.mid,
			Score:         match.similarity,
		}
	}
	medianSimilarity := semanticMedian(similarities)
	if medianSimilarity < minSimilarity+0.025 {
		return Alignment{}, fmt.Errorf("semantic subtitle similarity is marginal (median %.3f); refusing an ambiguous match", medianSimilarity)
	}

	fit := timeline.Piecewise(anchors, targetLast, timeline.Options{
		MinJumpSeconds: opts.MinGapSeconds,
		MaxSegments:    opts.MaxSegments,
		MinAnchors:     3,
	})
	if opts.DisablePiecewise {
		fit = timeline.Piecewise(anchors, targetLast, timeline.Options{MinJumpSeconds: opts.MinGapSeconds, MaxSegments: 1, MinAnchors: 3})
	}
	if fit.Scale < 0.8 || fit.Scale > 1.2 {
		return Alignment{}, fmt.Errorf("semantic subtitle timing scale %.6f is outside the safe 0.8-1.2 range", fit.Scale)
	}
	refineSemanticBoundaries(&fit, anchors)

	// Refit after removing isolated semantic anchors that disagree with the
	// fitted timeline. A real edit creates a supported plateau; a mistranslated
	// or repeated line creates a lone residual and is discarded.
	clean := make([]timeline.Anchor, 0, len(anchors))
	for _, anchor := range anchors {
		if semanticFitResidualMS(fit, anchor) <= 3500 {
			clean = append(clean, anchor)
		}
	}
	if len(clean) < minimum || len(clean)*5 < len(anchors)*4 {
		return Alignment{}, fmt.Errorf("semantic subtitle anchors are inconsistent (%d/%d survive timing validation across %d segment(s), residual %dms)", len(clean), len(anchors), len(fit.Segments), fit.ResidualMS)
	}
	fit = timeline.Piecewise(clean, targetLast, timeline.Options{
		MinJumpSeconds: opts.MinGapSeconds,
		MaxSegments:    opts.MaxSegments,
		MinAnchors:     3,
	})
	if opts.DisablePiecewise {
		fit = timeline.Piecewise(clean, targetLast, timeline.Options{MinJumpSeconds: opts.MinGapSeconds, MaxSegments: 1, MinAnchors: 3})
	}
	refineSemanticBoundaries(&fit, clean)
	protectSemanticGapCues(reference, target, &fit)
	if fit.ResidualMS > 1500 {
		return Alignment{}, fmt.Errorf("semantic subtitle timing residual is too high (%dms > 1500ms)", fit.ResidualMS)
	}

	alignment := Alignment{
		OffsetMS: fit.OffsetMS, Scale: fit.Scale, Score: medianSimilarity,
		OriginalScore: seed.OriginalScore, ReferenceCues: len(reference), TargetCues: len(target),
		Samples: fit.Samples, ResidualMS: fit.ResidualMS, Segments: fit.Segments, Gaps: fit.Gaps,
		Method: "semantic", PreserveTargetCues: true,
	}
	return alignment, nil
}

// protectSemanticGapCues keeps a sparse semantic boundary from deleting
// dialogue that already has strong temporal support on either side of the
// proposed cut. Cross-language cue splitting makes the exact edit boundary
// much noisier than the surrounding semantic anchors; the timeline jump can
// still be correct while its midpoint lands on a legitimate short cue.
func protectSemanticGapCues(reference, target []Cue, fit *timeline.Fit) {
	if fit == nil || len(reference) == 0 || len(target) == 0 {
		return
	}
	for gapIndex := range fit.Gaps {
		gap := fit.Gaps[gapIndex]
		if gap.DeltaMS >= 0 || gapIndex+1 >= len(fit.Segments) {
			continue
		}
		left, right := fit.Segments[gapIndex], fit.Segments[gapIndex+1]
		scale := right.Scale
		if scale <= 0 {
			scale = 1
		}
		durationSeconds := float64(gap.DurationMS) / 1000 / scale
		original := float64(gap.TargetAtMS) / 1000
		candidate := original
		maxMove := math.Max(5, math.Min(30, durationSeconds*4))
		for attempt := 0; attempt < 4; attempt++ {
			lower, upper := math.Inf(-1), math.Inf(1)
			for _, cue := range target {
				mid := float64(cue.Start+cue.End) / 2 / float64(time.Second)
				if mid < candidate || mid >= candidate+durationSeconds {
					continue
				}
				leftSupport := semanticCueTemporalSupport(reference, cue, left)
				rightSupport := semanticCueTemporalSupport(reference, cue, right)
				if leftSupport >= rightSupport {
					lower = math.Max(lower, float64(cue.End)/float64(time.Second))
				} else {
					upper = math.Min(upper, float64(cue.Start)/float64(time.Second)-durationSeconds)
				}
			}
			next := candidate
			if next < lower {
				next = lower
			}
			if next > upper {
				next = upper
			}
			if lower > upper || math.Abs(next-original) > maxMove || math.Abs(next-candidate) < 0.0005 {
				break
			}
			candidate = next
		}
		if math.Abs(candidate-original) >= 0.0005 {
			timeline.SetBoundary(fit, gapIndex, candidate)
		}
	}
}

// PreserveCrossLanguageCues prepares a language-independent activity fit for
// translated subtitle rendering. Activity can prove a timeline discontinuity,
// but cue-count differences cannot prove that target dialogue is disposable:
// translators routinely split, merge, or add short explanatory cues. Keep all
// target dialogue and move noisy cut boundaries away from supported cues.
func PreserveCrossLanguageCues(reference, target []Cue, alignment Alignment) Alignment {
	fit := timeline.Fit{
		Scale: alignment.Scale, OffsetMS: alignment.OffsetMS, Score: alignment.Score,
		Samples: alignment.Samples, ResidualMS: alignment.ResidualMS,
		Segments: append([]timeline.Segment(nil), alignment.Segments...),
		Gaps:     append([]timeline.Gap(nil), alignment.Gaps...),
	}
	protectSemanticGapCues(reference, target, &fit)
	alignment.Segments = fit.Segments
	alignment.Gaps = fit.Gaps
	alignment.PreserveTargetCues = true
	return alignment
}

func semanticCueTemporalSupport(reference []Cue, cue Cue, segment timeline.Segment) float64 {
	scale := segment.Scale
	if scale <= 0 {
		scale = 1
	}
	start := float64(cue.Start)/float64(time.Second)*scale + float64(segment.OffsetMS)/1000
	end := float64(cue.End)/float64(time.Second)*scale + float64(segment.OffsetMS)/1000
	if end <= start {
		return 0
	}
	best := 0.0
	for _, candidate := range reference {
		refStart := float64(candidate.Start) / float64(time.Second)
		refEnd := float64(candidate.End) / float64(time.Second)
		overlap := math.Max(0, math.Min(end, refEnd)-math.Max(start, refStart))
		if overlap > 0 {
			score := 2 * overlap / ((end - start) + (refEnd - refStart))
			best = math.Max(best, score)
		}
		// Translations commonly start or end a short line one second away from
		// the original even when both describe the same spoken phrase. Exact
		// overlap alone would classify those edge differences as removable
		// footage, so retain bounded midpoint proximity as weaker support.
		midpointDistance := math.Abs((start+end)/2 - (refStart+refEnd)/2)
		if midpointDistance < 2.5 {
			best = math.Max(best, 1-midpointDistance/2.5)
		}
	}
	return best
}

func semanticSeed(reference, target []Cue, opts AlignOptions) Alignment {
	seedOpts := opts
	seedOpts.MinScore = 0.000001
	seedOpts.DisablePiecewise = true
	if seed, err := Align(reference, target, seedOpts); err == nil {
		return seed
	}
	refFirst, refLast := cueBounds(reference)
	targetFirst, targetLast := cueBounds(target)
	scale := (refLast - refFirst) / math.Max(1, targetLast-targetFirst)
	if scale < 0.8 || scale > 1.2 {
		scale = 1
	}
	return Alignment{Scale: scale, OffsetMS: int(math.Round((refFirst - scale*targetFirst) * 1000))}
}

func semanticUnits(cues []Cue, maxBlock int) []semanticUnit {
	units := make([]semanticUnit, 0, len(cues)*maxBlock)
	for first := range cues {
		var lines []string
		for size := 1; size <= maxBlock && first+size <= len(cues); size++ {
			cue := cues[first+size-1]
			lines = append(lines, cue.Text...)
			text := normalizeSemanticText(strings.Join(lines, " "))
			if text == "" {
				continue
			}
			start := float64(cues[first].Start) / float64(time.Second)
			end := float64(cue.End) / float64(time.Second)
			units = append(units, semanticUnit{first: first, last: first + size - 1, mid: (start + end) / 2, text: text})
		}
	}
	return units
}

func normalizeSemanticText(text string) string {
	var out []rune
	inTag := false
	for _, r := range text {
		switch r {
		case '<', '{':
			inTag = true
		case '>', '}':
			inTag = false
		default:
			if inTag {
				continue
			}
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				out = append(out, unicode.ToLower(r))
			} else if len(out) > 0 && out[len(out)-1] != ' ' {
				out = append(out, ' ')
			}
		}
	}
	return strings.TrimSpace(string(out))
}

func mutualSemanticMatches(reference, target []semanticUnit, seed Alignment, window, minSimilarity, minMargin float64) []semanticMatch {
	bestRefForTarget := make([]int, len(target))
	bestTargetForRef := make([]int, len(reference))
	bestTargetScore := make([]float64, len(target))
	secondTargetScore := make([]float64, len(target))
	bestRefScore := make([]float64, len(reference))
	secondRefScore := make([]float64, len(reference))
	for i := range bestRefForTarget {
		bestRefForTarget[i] = -1
		bestTargetScore[i], secondTargetScore[i] = -2, -2
	}
	for i := range bestTargetForRef {
		bestTargetForRef[i], bestRefScore[i], secondRefScore[i] = -1, -2, -2
	}
	scale := seed.Scale
	if scale <= 0 {
		scale = 1
	}
	offset := float64(seed.OffsetMS) / 1000
	for ti, targetUnit := range target {
		expected := targetUnit.mid*scale + offset
		for ri, refUnit := range reference {
			if math.Abs(refUnit.mid-expected) > window {
				continue
			}
			score := cosine(targetUnit.vector, refUnit.vector)
			if score > bestTargetScore[ti] {
				secondTargetScore[ti] = bestTargetScore[ti]
				bestTargetScore[ti], bestRefForTarget[ti] = score, ri
			} else if score > secondTargetScore[ti] {
				secondTargetScore[ti] = score
			}
			if score > bestRefScore[ri] {
				secondRefScore[ri] = bestRefScore[ri]
				bestRefScore[ri], bestTargetForRef[ri] = score, ti
			} else if score > secondRefScore[ri] {
				secondRefScore[ri] = score
			}
		}
	}
	var matches []semanticMatch
	for ti, ri := range bestRefForTarget {
		if ri < 0 || bestTargetForRef[ri] != ti {
			continue
		}
		margin := math.Min(bestTargetScore[ti]-secondTargetScore[ti], bestRefScore[ri]-secondRefScore[ri])
		if bestTargetScore[ti] < minSimilarity || margin < minMargin {
			continue
		}
		matches = append(matches, semanticMatch{target: target[ti], reference: reference[ri], similarity: bestTargetScore[ti], margin: margin})
	}
	return matches
}

func monotonicSemanticMatches(matches []semanticMatch) []semanticMatch {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].target.mid == matches[j].target.mid {
			return matches[i].reference.mid < matches[j].reference.mid
		}
		return matches[i].target.mid < matches[j].target.mid
	})
	if len(matches) == 0 {
		return nil
	}
	weights := make([]float64, len(matches))
	previous := make([]int, len(matches))
	for i := range matches {
		weights[i], previous[i] = matches[i].similarity+matches[i].margin, -1
		for j := 0; j < i; j++ {
			if matches[j].target.last >= matches[i].target.first || matches[j].reference.last >= matches[i].reference.first {
				continue
			}
			candidate := weights[j] + matches[i].similarity + matches[i].margin
			if candidate > weights[i] {
				weights[i], previous[i] = candidate, j
			}
		}
	}
	best := 0
	for i := 1; i < len(weights); i++ {
		if weights[i] > weights[best] {
			best = i
		}
	}
	var chain []semanticMatch
	for at := best; at >= 0; at = previous[at] {
		chain = append(chain, matches[at])
		if previous[at] < 0 {
			break
		}
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func semanticCoverage(matches []semanticMatch, first, last float64) float64 {
	if len(matches) < 2 || last <= first {
		return 0
	}
	return math.Min(1, (matches[len(matches)-1].target.mid-matches[0].target.mid)/(last-first))
}

func semanticCoverageBuckets(matches []semanticMatch, first, last float64) int {
	if last <= first {
		return 0
	}
	var occupied [4]bool
	for _, match := range matches {
		bucket := int((match.target.mid - first) / (last - first) * 4)
		bucket = max(0, min(3, bucket))
		occupied[bucket] = true
	}
	count := 0
	for _, yes := range occupied {
		if yes {
			count++
		}
	}
	return count
}

func semanticFitResidualMS(fit timeline.Fit, anchor timeline.Anchor) int {
	if len(fit.Segments) == 0 {
		return math.MaxInt
	}
	targetMS := int(math.Round(anchor.TargetSeconds * 1000))
	segment := fit.Segments[len(fit.Segments)-1]
	for _, candidate := range fit.Segments {
		if targetMS < candidate.TargetEndMS {
			segment = candidate
			break
		}
	}
	predicted := float64(targetMS)*segment.Scale + float64(segment.OffsetMS)
	actual := (anchor.TargetSeconds + anchor.DelaySeconds) * 1000
	return int(math.Round(math.Abs(predicted - actual)))
}

func refineSemanticBoundaries(fit *timeline.Fit, anchors []timeline.Anchor) {
	if fit == nil || len(fit.Gaps) == 0 || len(anchors) < 6 {
		return
	}
	for gapIndex := range fit.Gaps {
		left, right := fit.Segments[gapIndex], fit.Segments[gapIndex+1]
		bestAt, bestLoss := -1, math.Inf(1)
		for at := 3; at <= len(anchors)-3; at++ {
			loss := 0.0
			for i, anchor := range anchors {
				segment := right
				if i < at {
					segment = left
				}
				targetMS := anchor.TargetSeconds * 1000
				predictedReferenceMS := targetMS*segment.Scale + float64(segment.OffsetMS)
				actualReferenceMS := (anchor.TargetSeconds + anchor.DelaySeconds) * 1000
				loss += math.Min(10_000, math.Abs(predictedReferenceMS-actualReferenceMS))
			}
			if loss < bestLoss {
				bestAt, bestLoss = at, loss
			}
		}
		if bestAt > 0 {
			boundary := (anchors[bestAt-1].TargetSeconds + anchors[bestAt].TargetSeconds) / 2
			timeline.SetBoundary(fit, gapIndex, boundary)
		}
	}
}

func semanticMedian(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	middle := len(copyValues) / 2
	if len(copyValues)%2 == 0 {
		return (copyValues[middle-1] + copyValues[middle]) / 2
	}
	return copyValues[middle]
}

func cosine(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return -1
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return -1
	}
	return dot / math.Sqrt(normA*normB)
}
