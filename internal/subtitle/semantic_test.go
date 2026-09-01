package subtitle

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

type conceptEmbedder struct {
	constant bool
}

func (e conceptEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	vectors := make([][]float64, len(texts))
	for i, text := range texts {
		vectors[i] = make([]float64, 2048)
		if e.constant {
			vectors[i][0] = 1
			continue
		}
		for _, field := range strings.Fields(text) {
			id, err := strconv.Atoi(strings.TrimPrefix(field, "concept"))
			if err == nil && id >= 0 && id < len(vectors[i]) {
				vectors[i][id] = 1
			}
		}
	}
	return vectors, nil
}

func semanticCue(start, end float64, language string, concepts ...int) Cue {
	words := make([]string, len(concepts))
	for i, concept := range concepts {
		words[i] = fmt.Sprintf("%s concept%d", language, concept)
	}
	return Cue{
		Start: time.Duration(start * float64(time.Second)),
		End:   time.Duration(end * float64(time.Second)),
		Text:  []string{strings.Join(words, " ")},
	}
}

func TestAlignSemanticHandlesTranslationSegmentationMissingLinesAndFPS(t *testing.T) {
	const scale = 25 / (24000.0 / 1001)
	const offset = -3.4
	var reference, target []Cue
	for i := 0; i < 140; {
		start := 12 + float64(i)*5.73 + float64((i*i)%17)*0.11
		duration := .7 + float64((i*7)%9)*.08
		reference = append(reference, semanticCue(start, start+duration, "english", i))
		if i%17 == 8 { // a translated release can omit signs or redundant lines
			i++
			continue
		}
		if i%13 == 4 && i+1 < 140 { // two English cues translated as one cue
			nextStart := 12 + float64(i+1)*5.73 + float64(((i+1)*(i+1))%17)*0.11
			nextDuration := .7 + float64(((i+1)*7)%9)*.08
			reference = append(reference, semanticCue(nextStart, nextStart+nextDuration, "english", i+1))
			target = append(target, semanticCue((start-offset)/scale, (nextStart+nextDuration-offset)/scale, "tamil", i, i+1))
			i += 2
			continue
		}
		target = append(target, semanticCue((start-offset)/scale, (start+duration-offset)/scale, "tamil", i))
		i++
	}

	alignment, err := AlignSemantic(context.Background(), reference, target, conceptEmbedder{}, SemanticOptions{
		AlignOptions:  AlignOptions{MaxOffsetSeconds: 30, MinGapSeconds: .35, MaxSegments: 6},
		MinSimilarity: .80, MinMargin: .10, SearchWindowSeconds: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	if alignment.Method != "semantic" || alignment.Samples < 50 {
		t.Fatalf("weak semantic result: %+v", alignment)
	}
	if math.Abs(alignment.Scale-scale) > .00025 {
		t.Fatalf("scale %.9f, want %.9f", alignment.Scale, scale)
	}
	if math.Abs(float64(alignment.OffsetMS)/1000-offset) > .12 {
		t.Fatalf("offset %dms, want %.0fms", alignment.OffsetMS, offset*1000)
	}
	if alignment.ResidualMS > 150 {
		t.Fatalf("residual %dms", alignment.ResidualMS)
	}
}

func TestAlignSemanticFindsTargetOnlyEdit(t *testing.T) {
	var reference, target []Cue
	for i := 0; i < 150; i++ {
		refStart := 10 + float64(i)*5.4 + float64((i*i)%11)*.09
		targetStart := refStart + 2
		if refStart >= 405 {
			targetStart += 6
		}
		reference = append(reference, semanticCue(refStart, refStart+.9, "english", i))
		target = append(target, semanticCue(targetStart, targetStart+.9, "telugu", i))
	}
	alignment, err := AlignSemantic(context.Background(), reference, target, conceptEmbedder{}, SemanticOptions{
		AlignOptions:  AlignOptions{MaxOffsetSeconds: 30, MinGapSeconds: .35, MaxSegments: 6},
		MinSimilarity: .80, MinMargin: .10, SearchWindowSeconds: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(alignment.Gaps) != 1 || alignment.Gaps[0].Action != "remove_target" {
		t.Fatalf("gaps: %#v", alignment.Gaps)
	}
	if math.Abs(float64(alignment.Gaps[0].DurationMS-6000)) > 150 {
		t.Fatalf("gap: %#v", alignment.Gaps[0])
	}
}

func TestAlignSemanticRejectsDifferentProgramme(t *testing.T) {
	var reference, target []Cue
	for i := 0; i < 100; i++ {
		start := 10 + float64(i)*6.2
		reference = append(reference, semanticCue(start, start+1, "english", i))
		target = append(target, semanticCue(start+2, start+3, "hindi", 1000+i))
	}
	_, err := AlignSemantic(context.Background(), reference, target, conceptEmbedder{}, SemanticOptions{
		AlignOptions: AlignOptions{MaxOffsetSeconds: 30}, MinSimilarity: .80, MinMargin: .10,
	})
	if err == nil || !strings.Contains(err.Error(), "confidence is too low") {
		t.Fatalf("expected strict mismatch rejection, got %v", err)
	}
}

func TestAlignSemanticRejectsAmbiguousRepeatedDialogue(t *testing.T) {
	var reference, target []Cue
	for i := 0; i < 100; i++ {
		start := 10 + float64(i)*6.2
		reference = append(reference, semanticCue(start, start+1, "english", i))
		target = append(target, semanticCue(start+2, start+3, "tamil", i))
	}
	_, err := AlignSemantic(context.Background(), reference, target, conceptEmbedder{constant: true}, SemanticOptions{
		AlignOptions: AlignOptions{MaxOffsetSeconds: 30}, MinSimilarity: .80, MinMargin: .10,
	})
	if err == nil || !strings.Contains(err.Error(), "confidence is too low") {
		t.Fatalf("expected ambiguity rejection, got %v", err)
	}
}

func TestAlignSemanticRejectsMatchesClusteredAtEdges(t *testing.T) {
	var reference, target []Cue
	for i := 0; i < 120; i++ {
		start := 10 + float64(i)*6.2
		targetConcept := 1000 + i
		if i < 15 || i >= 105 {
			targetConcept = i
		}
		reference = append(reference, semanticCue(start, start+1, "english", i))
		target = append(target, semanticCue(start+2, start+3, "hindi", targetConcept))
	}
	_, err := AlignSemantic(context.Background(), reference, target, conceptEmbedder{}, SemanticOptions{
		AlignOptions: AlignOptions{MaxOffsetSeconds: 30}, MinSimilarity: .80, MinMargin: .10,
	})
	if err == nil || !strings.Contains(err.Error(), "programme regions") {
		t.Fatalf("expected clustered coverage rejection, got %v", err)
	}
}
