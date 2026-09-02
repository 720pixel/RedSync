package subtitle

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

type fixedAnchorMatcher struct {
	pairs []SemanticAnchorPair
	calls int
}

func (m *fixedAnchorMatcher) Match(context.Context, []Cue, []Cue, Alignment, SemanticOptions) ([]SemanticAnchorPair, error) {
	m.calls++
	return append([]SemanticAnchorPair(nil), m.pairs...), nil
}

func TestAlignCodexSemanticFits25FPSHindiSDHTo23976English(t *testing.T) {
	const offset = -3.4
	scale := 25.0 / (24000.0 / 1001.0)
	var reference, target []Cue
	var pairs []SemanticAnchorPair
	for i := 0; i < 120; i++ {
		refStart := 12.0 + float64(i)*5.7
		refEnd := refStart + 1.35
		targetStart := (refStart - offset) / scale
		targetEnd := (refEnd - offset) / scale
		if i%10 == 0 {
			target = append(target, cueSeconds(targetStart-0.70, targetStart-0.20, "[संगीत बजता है]"))
		}
		targetIndex := len(target)
		reference = append(reference, cueSeconds(refStart, refEnd, fmt.Sprintf("Agent Roman confirms checkpoint %d", i+100)))
		target = append(target, cueSeconds(targetStart, targetEnd, "एजेंट रोमन चौकी की पुष्टि करता है"))
		if i%5 == 0 {
			pairs = append(pairs, SemanticAnchorPair{
				ReferenceFirst: i, ReferenceLast: i,
				TargetFirst: targetIndex, TargetLast: targetIndex,
			})
		}
	}

	matcher := &fixedAnchorMatcher{pairs: pairs}
	alignment, err := AlignCodexSemantic(context.Background(), reference, target, matcher, SemanticOptions{
		AlignOptions: AlignOptions{MaxOffsetSeconds: 60, MinGapSeconds: .35, MaxSegments: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	if alignment.Method != "semantic-codex" {
		t.Fatalf("method = %q", alignment.Method)
	}
	if math.Abs(alignment.Scale-scale) > 0.00002 {
		t.Fatalf("scale = %.9f, want %.9f", alignment.Scale, scale)
	}
	if math.Abs(float64(alignment.OffsetMS)-offset*1000) > 15 {
		t.Fatalf("offset = %dms, want %.0fms", alignment.OffsetMS, offset*1000)
	}
	if alignment.ResidualMS > 5 {
		t.Fatalf("residual = %dms", alignment.ResidualMS)
	}
	if alignment.Samples != len(pairs) {
		t.Fatalf("samples = %d, want %d", alignment.Samples, len(pairs))
	}
}

func TestCodexPromptKeepsAIOutOfTimingMath(t *testing.T) {
	var reference, target []Cue
	for i := 0; i < 60; i++ {
		at := 10.0 + float64(i)*7
		reference = append(reference, cueSeconds(at, at+1.5, fmt.Sprintf("Detective Rivera found evidence number %d at checkpoint Delta", i+47)))
		target = append(target, cueSeconds(at+4, at+5.5, "जासूस रिवेरा को डेल्टा चौकी पर सबूत संख्या 47 मिला"))
	}
	m := &CodexAnchorMatcher{MaxAnchors: 12}
	prompt, refs, targets, err := m.buildPrompt(reference, target, Alignment{Scale: 1, OffsetMS: -4000}, SemanticOptions{
		AlignOptions: AlignOptions{MaxOffsetSeconds: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"sparse subtitle dialogue anchors",
		"Ignore target-only SDH descriptions",
		"23.976/24/25 fps",
		"Timestamps only narrow candidates; do not calculate timing",
		"globally chronological",
		"Never invent IDs",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt is missing %q", required)
		}
	}
	if len(refs) < 6 || len(targets) < 6 {
		t.Fatalf("sparse prompt has refs=%d targets=%d", len(refs), len(targets))
	}
	if len(prompt) > 200_000 {
		t.Fatalf("prompt unexpectedly large: %d bytes", len(prompt))
	}
}

func TestCodexMatcherCachesPairsAcrossTimestampOnlyVerification(t *testing.T) {
	var reference, target []Cue
	for i := 0; i < 60; i++ {
		at := 20.0 + float64(i)*6
		reference = append(reference, cueSeconds(at, at+1, fmt.Sprintf("Captain Mira confirms sector %d", i+200)))
		target = append(target, cueSeconds(at+3, at+4, "कप्तान मीरा सेक्टर की पुष्टि करती हैं"))
	}
	calls := 0
	m := &CodexAnchorMatcher{MaxAnchors: 12}
	prompt, refs, targets, err := m.buildPrompt(reference, target, Alignment{Scale: 1, OffsetMS: -3000}, SemanticOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var referenceID, targetID string
	for id := range refs {
		referenceID = id
		break
	}
	for id := range targets {
		targetID = id
		break
	}
	if prompt == "" || referenceID == "" || targetID == "" {
		t.Fatal("failed to build prompt fixture")
	}
	m.Run = func(context.Context, string, []byte) ([]byte, error) {
		calls++
		return []byte(`{"matches":[{"reference_id":"` + referenceID + `","target_id":"` + targetID + `"}]}`), nil
	}
	if _, err := m.Match(context.Background(), reference, target, Alignment{}, SemanticOptions{}); err != nil {
		t.Fatal(err)
	}
	retimed := append([]Cue(nil), target...)
	for i := range retimed {
		retimed[i].Start -= 3 * time.Second
		retimed[i].End -= 3 * time.Second
	}
	if _, err := m.Match(context.Background(), reference, retimed, Alignment{}, SemanticOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("Codex calls = %d, want one semantic call plus cached verification", calls)
	}
}

func TestCodexMatcherRebindsCachedPairsAfterUnanchoredCueRemoval(t *testing.T) {
	var reference, target []Cue
	for i := 0; i < 60; i++ {
		at := 20.0 + float64(i)*6
		reference = append(reference, cueSeconds(at, at+1, fmt.Sprintf("Captain Mira confirms sector %d", i+200)))
		target = append(target, cueSeconds(at+3, at+4, fmt.Sprintf("translated distinctive line %d", i+200)))
	}
	pairs := []SemanticAnchorPair{
		{ReferenceFirst: 5, ReferenceLast: 5, TargetFirst: 5, TargetLast: 5},
		{ReferenceFirst: 15, ReferenceLast: 15, TargetFirst: 15, TargetLast: 15},
		{ReferenceFirst: 25, ReferenceLast: 25, TargetFirst: 25, TargetLast: 25},
		{ReferenceFirst: 35, ReferenceLast: 35, TargetFirst: 35, TargetLast: 35},
		{ReferenceFirst: 45, ReferenceLast: 45, TargetFirst: 45, TargetLast: 45},
		{ReferenceFirst: 55, ReferenceLast: 55, TargetFirst: 55, TargetLast: 55},
	}
	calls := 0
	m := &CodexAnchorMatcher{
		cacheKey: semanticCueSetKey(reference, target), cache: pairs,
		cacheRef: semanticCueTexts(reference), cacheTgt: semanticCueTexts(target),
		Run: func(context.Context, string, []byte) ([]byte, error) {
			calls++
			return nil, fmt.Errorf("unexpected Codex call")
		},
	}
	rendered := append([]Cue(nil), target[:10]...)
	rendered = append(rendered, target[11:]...)
	rebound, err := m.Match(context.Background(), reference, rendered, Alignment{}, SemanticOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("Codex calls = %d, want cached rebind without another call", calls)
	}
	if len(rebound) != len(pairs) || rebound[1].TargetFirst != 14 || rebound[len(rebound)-1].TargetFirst != 54 {
		t.Fatalf("rebound pairs = %#v", rebound)
	}
}

func cueSeconds(start, end float64, text string) Cue {
	return Cue{
		Start: time.Duration(math.Round(start * float64(time.Second))),
		End:   time.Duration(math.Round(end * float64(time.Second))),
		Text:  []string{text},
	}
}
