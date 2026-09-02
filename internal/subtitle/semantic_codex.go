package subtitle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/720pixel/RedSync/internal/timeline"
)

// SemanticAnchorPair identifies dialogue that means the same thing in the
// English reference and translated target. Index ranges allow a translation
// to split or merge adjacent cues.
type SemanticAnchorPair struct {
	ReferenceFirst int
	ReferenceLast  int
	TargetFirst    int
	TargetLast     int
}

// SemanticAnchorMatcher supplies sparse meaning matches. It does not calculate
// offsets, FPS conversion, gaps, or rendering timestamps; those remain
// deterministic RedSync responsibilities.
type SemanticAnchorMatcher interface {
	Match(context.Context, []Cue, []Cue, Alignment, SemanticOptions) ([]SemanticAnchorPair, error)
}

// CodexAnchorMatcher invokes a short-lived, subscription-authenticated Codex
// CLI process for the extreme no-audio/no-English-target case. Only sparse cue
// text and coarse timestamps are sent. The process starts in the system temp
// directory, is ephemeral and read-only, and receives a strict JSON schema.
type CodexAnchorMatcher struct {
	Binary          string
	Model           string
	ReasoningEffort string
	Timeout         time.Duration
	MaxAnchors      int

	// Run is a test seam. Production leaves it nil and uses codex exec.
	Run func(context.Context, string, []byte) ([]byte, error)

	mu       sync.Mutex
	cacheKey string
	cache    []SemanticAnchorPair
	cacheRef []string
	cacheTgt []string
}

type codexCueUnit struct {
	id          string
	first, last int
	mid         float64
	text        string
}

type codexMatchResponse struct {
	Matches []struct {
		ReferenceID string `json:"reference_id"`
		TargetID    string `json:"target_id"`
	} `json:"matches"`
}

var codexSemanticSlot = make(chan struct{}, 1)

// AlignCodexSemantic uses AI only to identify sparse translated dialogue. A
// robust monotonic timing fit then estimates exact delay, FPS/clock scale and
// internal edits. Wrong/repeated lines are removed by timing consistency.
func AlignCodexSemantic(ctx context.Context, reference, target []Cue, matcher SemanticAnchorMatcher, opts SemanticOptions) (Alignment, error) {
	if matcher == nil {
		return Alignment{}, fmt.Errorf("semantic subtitle alignment requires an anchor matcher")
	}
	if len(reference) < 6 || len(target) < 6 {
		return Alignment{}, fmt.Errorf("semantic subtitle alignment needs at least 6 cues in both files")
	}
	seed := semanticSeed(reference, target, opts.AlignOptions)
	pairs, err := matcher.Match(ctx, reference, target, seed, opts)
	if err != nil {
		return Alignment{}, fmt.Errorf("Codex semantic anchors: %w", err)
	}
	pairs = monotonicAnchorPairs(validAnchorPairs(pairs, len(reference), len(target)))
	minimum := max(6, min(12, min(len(reference), len(target))/30))
	if len(pairs) < minimum {
		return Alignment{}, fmt.Errorf("semantic subtitle evidence is insufficient: %d consistent anchors, need %d", len(pairs), minimum)
	}

	anchors := make([]timeline.Anchor, 0, len(pairs))
	for _, pair := range pairs {
		refMid := cueRangeMid(reference, pair.ReferenceFirst, pair.ReferenceLast)
		targetMid := cueRangeMid(target, pair.TargetFirst, pair.TargetLast)
		anchors = append(anchors, timeline.Anchor{TargetSeconds: targetMid, DelaySeconds: refMid - targetMid, Score: 1})
	}
	targetFirst, targetLast := cueBounds(target)
	if semanticAnchorCoverage(anchors, targetFirst, targetLast) < 0.45 {
		return Alignment{}, fmt.Errorf("semantic anchors do not span enough of the programme")
	}
	if semanticAnchorBuckets(anchors, targetFirst, targetLast) < 3 {
		return Alignment{}, fmt.Errorf("semantic anchors do not cover at least three programme regions")
	}

	fit := fitSemanticTimeline(anchors, targetLast, opts)
	if fit.Scale < 0.8 || fit.Scale > 1.2 {
		return Alignment{}, fmt.Errorf("semantic subtitle timing scale %.6f is outside the safe 0.8-1.2 range", fit.Scale)
	}
	refineSemanticBoundaries(&fit, anchors)

	clean := make([]timeline.Anchor, 0, len(anchors))
	for _, anchor := range anchors {
		if semanticFitResidualMS(fit, anchor) <= 3500 {
			clean = append(clean, anchor)
		}
	}
	// Sparse cross-language matches are deliberately broad enough to cover the
	// whole programme. Translation splits, repeated names, and nearby candidate
	// lines can make a minority of otherwise sensible pairs timing outliers.
	// Two-thirds consensus with the normal minimum/coverage checks is strong
	// evidence; rendering and an independent bounded verification still follow.
	if len(clean) < minimum || len(clean)*3 < len(anchors)*2 {
		return Alignment{}, fmt.Errorf("semantic anchors disagree on timing (%d/%d survive deterministic validation)", len(clean), len(anchors))
	}
	fit = fitSemanticTimeline(clean, targetLast, opts)
	refineSemanticBoundaries(&fit, clean)
	protectSemanticGapCues(reference, target, &fit)
	if fit.Scale < 0.8 || fit.Scale > 1.2 {
		return Alignment{}, fmt.Errorf("semantic subtitle timing scale %.6f is outside the safe 0.8-1.2 range", fit.Scale)
	}
	if fit.ResidualMS > 1500 {
		return Alignment{}, fmt.Errorf("semantic subtitle timing residual is too high (%dms > 1500ms)", fit.ResidualMS)
	}

	consistency := float64(len(clean)) / float64(len(anchors))
	return Alignment{
		OffsetMS: fit.OffsetMS, Scale: fit.Scale, Score: consistency,
		OriginalScore: seed.OriginalScore, ReferenceCues: len(reference), TargetCues: len(target),
		Samples: fit.Samples, ResidualMS: fit.ResidualMS, Segments: fit.Segments, Gaps: fit.Gaps,
		Method: "semantic-codex", PreserveTargetCues: true,
	}, nil
}

func fitSemanticTimeline(anchors []timeline.Anchor, targetLast float64, opts SemanticOptions) timeline.Fit {
	maxSegments := opts.MaxSegments
	if opts.DisablePiecewise {
		maxSegments = 1
	}
	return timeline.Piecewise(anchors, targetLast, timeline.Options{
		MinJumpSeconds: opts.MinGapSeconds,
		MaxSegments:    maxSegments,
		MinAnchors:     3,
	})
}

func validAnchorPairs(pairs []SemanticAnchorPair, referenceCount, targetCount int) []SemanticAnchorPair {
	seenReference := make(map[[2]int]bool)
	seenTarget := make(map[[2]int]bool)
	out := make([]SemanticAnchorPair, 0, len(pairs))
	for _, pair := range pairs {
		if pair.ReferenceFirst < 0 || pair.ReferenceLast < pair.ReferenceFirst || pair.ReferenceLast >= referenceCount ||
			pair.TargetFirst < 0 || pair.TargetLast < pair.TargetFirst || pair.TargetLast >= targetCount {
			continue
		}
		refKey, targetKey := [2]int{pair.ReferenceFirst, pair.ReferenceLast}, [2]int{pair.TargetFirst, pair.TargetLast}
		if seenReference[refKey] || seenTarget[targetKey] {
			continue
		}
		seenReference[refKey], seenTarget[targetKey] = true, true
		out = append(out, pair)
	}
	return out
}

func monotonicAnchorPairs(pairs []SemanticAnchorPair) []SemanticAnchorPair {
	if len(pairs) < 2 {
		return pairs
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].TargetFirst != pairs[j].TargetFirst {
			return pairs[i].TargetFirst < pairs[j].TargetFirst
		}
		return pairs[i].ReferenceFirst < pairs[j].ReferenceFirst
	})
	length, previous := make([]int, len(pairs)), make([]int, len(pairs))
	best := 0
	for i := range pairs {
		length[i], previous[i] = 1, -1
		for j := 0; j < i; j++ {
			if pairs[j].TargetLast >= pairs[i].TargetFirst || pairs[j].ReferenceLast >= pairs[i].ReferenceFirst {
				continue
			}
			if length[j]+1 > length[i] {
				length[i], previous[i] = length[j]+1, j
			}
		}
		if length[i] > length[best] {
			best = i
		}
	}
	chain := make([]SemanticAnchorPair, 0, length[best])
	for at := best; at >= 0; at = previous[at] {
		chain = append(chain, pairs[at])
		if previous[at] < 0 {
			break
		}
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func cueRangeMid(cues []Cue, first, last int) float64 {
	return (float64(cues[first].Start) + float64(cues[last].End)) / (2 * float64(time.Second))
}

func semanticAnchorCoverage(anchors []timeline.Anchor, first, last float64) float64 {
	if len(anchors) < 2 || last <= first {
		return 0
	}
	return math.Min(1, (anchors[len(anchors)-1].TargetSeconds-anchors[0].TargetSeconds)/(last-first))
}

func semanticAnchorBuckets(anchors []timeline.Anchor, first, last float64) int {
	if last <= first {
		return 0
	}
	var occupied [4]bool
	for _, anchor := range anchors {
		bucket := int((anchor.TargetSeconds - first) / (last - first) * 4)
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

func (m *CodexAnchorMatcher) Match(ctx context.Context, reference, target []Cue, seed Alignment, opts SemanticOptions) ([]SemanticAnchorPair, error) {
	if m == nil {
		return nil, fmt.Errorf("Codex matcher is nil")
	}
	key := semanticCueSetKey(reference, target)
	m.mu.Lock()
	if key == m.cacheKey && len(m.cache) > 0 {
		cached := append([]SemanticAnchorPair(nil), m.cache...)
		m.mu.Unlock()
		return cached, nil
	}
	if rebound, ok := rebindSemanticPairs(m.cacheRef, semanticCueTexts(reference), m.cacheTgt, semanticCueTexts(target), m.cache); ok {
		m.mu.Unlock()
		return rebound, nil
	}
	m.mu.Unlock()

	prompt, referenceByID, targetByID, err := m.buildPrompt(reference, target, seed, opts)
	if err != nil {
		return nil, err
	}
	schema := codexAnchorSchema()
	var raw []byte
	if m.Run != nil {
		raw, err = m.Run(ctx, prompt, schema)
	} else {
		raw, err = m.runCodex(ctx, prompt, schema)
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("Codex returned an unexpectedly large response")
	}
	var response codexMatchResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode Codex JSON: %w", err)
	}
	pairs := make([]SemanticAnchorPair, 0, len(response.Matches))
	for _, match := range response.Matches {
		ref, refOK := referenceByID[match.ReferenceID]
		targetUnit, targetOK := targetByID[match.TargetID]
		if !refOK || !targetOK {
			continue
		}
		pairs = append(pairs, SemanticAnchorPair{
			ReferenceFirst: ref.first, ReferenceLast: ref.last,
			TargetFirst: targetUnit.first, TargetLast: targetUnit.last,
		})
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("Codex found no unambiguous translated anchors")
	}
	m.mu.Lock()
	m.cacheKey, m.cache = key, append([]SemanticAnchorPair(nil), pairs...)
	m.cacheRef, m.cacheTgt = semanticCueTexts(reference), semanticCueTexts(target)
	m.mu.Unlock()
	return pairs, nil
}

func semanticCueTexts(cues []Cue) []string {
	texts := make([]string, len(cues))
	for i, cue := range cues {
		texts[i] = normalizeSemanticText(strings.Join(cue.Text, " "))
	}
	return texts
}

// rebindSemanticPairs reuses the original semantic decisions after rendering
// when a genuine target-only section removed unanchored cues. Verification
// must not make a second, potentially different AI decision merely because
// cue indexes shifted; the retained cue text/order is the stable identity.
func rebindSemanticPairs(oldReference, reference, oldTarget, target []string, pairs []SemanticAnchorPair) ([]SemanticAnchorPair, bool) {
	if len(pairs) == 0 || len(oldReference) != len(reference) || len(target) > len(oldTarget) {
		return nil, false
	}
	for i := range reference {
		if reference[i] != oldReference[i] {
			return nil, false
		}
	}
	oldToNew := make([]int, len(oldTarget))
	for i := range oldToNew {
		oldToNew[i] = -1
	}
	newIndex := 0
	for oldIndex, text := range oldTarget {
		if newIndex < len(target) && text == target[newIndex] {
			oldToNew[oldIndex] = newIndex
			newIndex++
		}
	}
	if newIndex != len(target) {
		return nil, false
	}
	rebound := make([]SemanticAnchorPair, 0, len(pairs))
	for _, pair := range pairs {
		if pair.ReferenceFirst < 0 || pair.ReferenceLast >= len(reference) || pair.TargetFirst < 0 || pair.TargetLast >= len(oldToNew) {
			continue
		}
		first, last := oldToNew[pair.TargetFirst], oldToNew[pair.TargetLast]
		if first < 0 || last < first || last-first != pair.TargetLast-pair.TargetFirst {
			continue
		}
		rebound = append(rebound, SemanticAnchorPair{
			ReferenceFirst: pair.ReferenceFirst, ReferenceLast: pair.ReferenceLast,
			TargetFirst: first, TargetLast: last,
		})
	}
	if len(rebound) < 6 {
		return nil, false
	}
	return rebound, true
}

func (m *CodexAnchorMatcher) buildPrompt(reference, target []Cue, seed Alignment, opts SemanticOptions) (string, map[string]codexCueUnit, map[string]codexCueUnit, error) {
	maxAnchors := m.MaxAnchors
	if maxAnchors <= 0 {
		maxAnchors = 28
	}
	maxAnchors = max(8, min(40, maxAnchors))
	refIndexes := selectDistinctiveCues(reference, maxAnchors)
	if len(refIndexes) < 6 {
		return "", nil, nil, fmt.Errorf("not enough distinctive English cues for sparse matching")
	}
	referenceByID := make(map[string]codexCueUnit, len(refIndexes))
	for _, index := range refIndexes {
		id := fmt.Sprintf("R%05d", index)
		referenceByID[id] = codexCueUnit{id: id, first: index, last: index, mid: cueRangeMid(reference, index, index), text: cleanCodexCueText(reference[index])}
	}
	targetByID := selectTargetCandidateUnits(reference, target, refIndexes, seed, opts)
	if len(targetByID) < 6 {
		return "", nil, nil, fmt.Errorf("not enough translated cue candidates near the timing hypotheses")
	}

	refUnits := make([]codexCueUnit, 0, len(referenceByID))
	for _, unit := range referenceByID {
		refUnits = append(refUnits, unit)
	}
	sort.Slice(refUnits, func(i, j int) bool { return refUnits[i].first < refUnits[j].first })
	targetUnits := make([]codexCueUnit, 0, len(targetByID))
	for _, unit := range targetByID {
		targetUnits = append(targetUnits, unit)
	}
	sort.Slice(targetUnits, func(i, j int) bool {
		if targetUnits[i].first != targetUnits[j].first {
			return targetUnits[i].first < targetUnits[j].first
		}
		return targetUnits[i].last < targetUnits[j].last
	})

	var b strings.Builder
	b.WriteString("You match sparse subtitle dialogue anchors for a timing engine. Do not use tools or inspect files.\n")
	b.WriteString("Match English reference entries to target entries that clearly express the same spoken meaning, even when translated, paraphrased, or split/merged. ")
	b.WriteString("Ignore target-only SDH descriptions such as music, doors, sighs, speaker labels, and sound effects. Names, numbers, places, and distinctive multi-clause dialogue are strongest evidence. ")
	b.WriteString("The releases may have different delay, 23.976/24/25 fps timing, missing cues, and internal edits. Timestamps only narrow candidates; do not calculate timing or require equal timestamps. ")
	b.WriteString("Return a globally chronological, one-to-one set of only unambiguous matches, and never overlap target cue ranges. A target ID can represent two consecutive cues only when one translated utterance was genuinely split; prefer the single-cue ID whenever it fully carries the meaning, and never merge two separate statements. Skip repeated/short/uncertain dialogue. Never invent IDs. ")
	b.WriteString("Work through the programme from beginning to end and use names, numbers, objects, actions, and relationships to recover as many clear distributed anchors as the evidence supports. For matching releases, aim for 12-24 anchors rather than stopping after a small sample.\n\n")
	b.WriteString("ENGLISH REFERENCE ANCHORS\n")
	for _, unit := range refUnits {
		fmt.Fprintf(&b, "%s @ %.3fs | %s\n", unit.id, unit.mid, unit.text)
	}
	b.WriteString("\nTARGET CANDIDATES\n")
	for _, unit := range targetUnits {
		fmt.Fprintf(&b, "%s @ %.3fs | %s\n", unit.id, unit.mid, unit.text)
	}
	b.WriteString("\nReturn JSON matching the supplied schema. Aim for anchors distributed across the full programme; quality is more important than count.")
	return b.String(), referenceByID, targetByID, nil
}

func selectDistinctiveCues(cues []Cue, limit int) []int {
	if len(cues) == 0 {
		return nil
	}
	frequency := make(map[string]int)
	for _, cue := range cues {
		frequency[normalizeSemanticText(strings.Join(cue.Text, " "))]++
	}
	first, last := cueBounds(cues)
	span := math.Max(1, last-first)
	chosen := make([]int, 0, limit)
	used := make(map[int]bool)
	for bucket := 0; bucket < limit; bucket++ {
		bucketStart := first + span*float64(bucket)/float64(limit)
		bucketEnd := first + span*float64(bucket+1)/float64(limit)
		bestIndex, bestScore := -1, math.Inf(-1)
		for index, cue := range cues {
			mid := cueRangeMid(cues, index, index)
			if mid < bucketStart || (bucket < limit-1 && mid >= bucketEnd) {
				continue
			}
			text := cleanCodexCueText(cue)
			score := codexCueDistinctiveness(text, frequency[normalizeSemanticText(text)])
			if score > bestScore {
				bestIndex, bestScore = index, score
			}
		}
		if bestIndex >= 0 && bestScore >= 10 && !used[bestIndex] {
			used[bestIndex] = true
			chosen = append(chosen, bestIndex)
		}
	}
	sort.Ints(chosen)
	return chosen
}

func codexCueDistinctiveness(text string, frequency int) float64 {
	runes := []rune(text)
	letters, digits, words := 0, 0, len(strings.Fields(text))
	for _, r := range runes {
		if unicode.IsLetter(r) {
			letters++
		} else if unicode.IsDigit(r) {
			digits++
		}
	}
	if letters+digits < 8 || words < 2 || frequency > 2 {
		return -1
	}
	score := float64(min(letters+digits, 120)) + float64(min(words, 20))*2
	if digits > 0 {
		score += 12
	}
	if frequency > 1 {
		score -= 20
	}
	return score
}

func cleanCodexCueText(cue Cue) string {
	text := normalizeSemanticText(strings.Join(cue.Text, " "))
	runes := []rune(text)
	if len(runes) > 280 {
		text = string(runes[:280])
	}
	return text
}

func selectTargetCandidateUnits(reference, target []Cue, refIndexes []int, seed Alignment, opts SemanticOptions) map[string]codexCueUnit {
	units := semanticUnits(target, 2)
	targetFirst, targetLast := cueBounds(target)
	refFirst, refLast := cueBounds(reference)
	window := opts.SearchWindowSeconds
	if window <= 0 {
		window = math.Max(120, opts.MaxOffsetSeconds)
	}
	window = math.Max(45, math.Min(240, window))
	scale := seed.Scale
	if scale <= 0 {
		scale = 1
	}
	offset := float64(seed.OffsetMS) / 1000
	selected := make(map[string]codexCueUnit)
	for _, refIndex := range refIndexes {
		refMid := cueRangeMid(reference, refIndex, refIndex)
		seedExpected := (refMid - offset) / scale
		progress := (refMid - refFirst) / math.Max(1, refLast-refFirst)
		progressExpected := targetFirst + progress*(targetLast-targetFirst)
		type ranked struct {
			unit     semanticUnit
			distance float64
		}
		nearby := make([]ranked, 0, 24)
		for _, unit := range units {
			if codexUnitContainsNonDialogue(target, unit.first, unit.last) {
				continue
			}
			distance := math.Min(math.Abs(unit.mid-seedExpected), math.Abs(unit.mid-progressExpected))
			if distance <= window && len([]rune(unit.text)) >= 4 {
				nearby = append(nearby, ranked{unit: unit, distance: distance})
			}
		}
		sort.Slice(nearby, func(i, j int) bool {
			if nearby[i].distance != nearby[j].distance {
				return nearby[i].distance < nearby[j].distance
			}
			return len(nearby[i].unit.text) > len(nearby[j].unit.text)
		})
		for _, candidate := range nearby[:min(14, len(nearby))] {
			unit := candidate.unit
			id := fmt.Sprintf("T%05d_%05d", unit.first, unit.last)
			selected[id] = codexCueUnit{id: id, first: unit.first, last: unit.last, mid: unit.mid, text: unit.text}
		}
	}
	return selected
}

func codexUnitContainsNonDialogue(cues []Cue, first, last int) bool {
	for index := first; index <= last; index++ {
		text := strings.TrimSpace(strings.Join(cues[index].Text, " "))
		if text == "" {
			return true
		}
		runes := []rune(text)
		if len(runes) >= 2 && ((runes[0] == '[' && runes[len(runes)-1] == ']') ||
			(runes[0] == '(' && runes[len(runes)-1] == ')')) {
			return true
		}
		if strings.ContainsAny(text, "♪♫♬") {
			return true
		}
	}
	return false
}

func semanticCueSetKey(reference, target []Cue) string {
	h := sha256.New()
	for _, cues := range [][]Cue{reference, target} {
		for _, cue := range cues {
			h.Write([]byte(normalizeSemanticText(strings.Join(cue.Text, " "))))
			h.Write([]byte{0})
		}
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func codexAnchorSchema() []byte {
	return []byte(`{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "additionalProperties":false,
  "required":["matches"],
  "properties":{"matches":{"type":"array","maxItems":40,"items":{
    "type":"object","additionalProperties":false,
    "required":["reference_id","target_id"],
    "properties":{"reference_id":{"type":"string"},"target_id":{"type":"string"}}
  }}}
}`)
}

func (m *CodexAnchorMatcher) runCodex(ctx context.Context, prompt string, schema []byte) ([]byte, error) {
	binary := strings.TrimSpace(m.Binary)
	if binary == "" {
		binary = "codex"
	}
	model := strings.TrimSpace(m.Model)
	if model == "" {
		model = "gpt-5.4-mini"
	}
	effort := strings.TrimSpace(m.ReasoningEffort)
	if effort == "" {
		effort = "low"
	}
	switch effort {
	case "low", "medium", "high", "xhigh":
	default:
		return nil, fmt.Errorf("unsupported Codex reasoning effort %q", effort)
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	schemaFile, err := os.CreateTemp("", "redsync-codex-schema-*.json")
	if err != nil {
		return nil, fmt.Errorf("create Codex output schema: %w", err)
	}
	schemaPath := schemaFile.Name()
	defer os.Remove(schemaPath)
	if err := schemaFile.Chmod(0o600); err != nil {
		_ = schemaFile.Close()
		return nil, fmt.Errorf("secure Codex output schema: %w", err)
	}
	_, err = schemaFile.Write(schema)
	closeErr := schemaFile.Close()
	if err != nil {
		return nil, fmt.Errorf("write Codex output schema: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close Codex output schema: %w", closeErr)
	}

	select {
	case codexSemanticSlot <- struct{}{}:
		defer func() { <-codexSemanticSlot }()
	case <-runCtx.Done():
		return nil, fmt.Errorf("wait for Codex semantic slot: %w", runCtx.Err())
	}
	args := []string{
		"exec", "--skip-git-repo-check", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only", "--model", model, "--config", fmt.Sprintf("model_reasoning_effort=%q", effort),
		"--output-schema", schemaPath, "-",
	}
	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.Dir = filepath.Clean(os.TempDir())
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 600 {
			message = message[len(message)-600:]
		}
		if runCtx.Err() != nil {
			return nil, fmt.Errorf("Codex semantic timeout after %s", timeout)
		}
		return nil, fmt.Errorf("run Codex semantic matcher (%v): %s", err, message)
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}
