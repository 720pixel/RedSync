package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	rsync "github.com/720pixel/RedSync/internal/sync"
)

func TestStandaloneEventEmitterWritesPrefixedOneLineJSON(t *testing.T) {
	var stderr bytes.Buffer
	f := &standaloneFlags{eventsJSON: true, eventWriter: &stderr}
	emitter := newStandaloneEventEmitter(f, "audio", "/private/ref.m4a", "/private/ta.mka", "/work/ta.synced.mka", 2, 6)
	emitter.emit("measuring_complete", func(event *standaloneEvent) {
		setDriftEventMetrics(event, rsync.Drift{DelayMS: -125, Scale: 1, Score: 7.5, Samples: 12, ResidualMS: 9})
	})

	line, err := bufio.NewReader(&stderr).ReadString('\n')
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if !strings.HasPrefix(line, standaloneEventPrefix) {
		t.Fatalf("event prefix = %q", line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("event must be one line: %q", line)
	}

	var got standaloneEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, standaloneEventPrefix))), &got); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if got.SchemaVersion != 1 || got.Event != "measuring_complete" || got.Current != 2 || got.Index != 2 || got.Total != 6 {
		t.Fatalf("unexpected identity fields: %+v", got)
	}
	if got.Target != "ta.mka" || got.Message != "Analysis complete for ta.mka (2/6)" {
		t.Fatalf("missing stable progress fields: %+v", got)
	}
	if got.ReferenceBasename != "ref.m4a" || got.TargetBasename != "ta.mka" || got.OutputBasename != "ta.synced.mka" {
		t.Fatalf("paths were not reduced to basenames: %+v", got)
	}
	if got.SyncMS == nil || *got.SyncMS != -125 || got.Score == nil || *got.Score != 7.5 {
		t.Fatalf("missing measurement metrics: %+v", got)
	}
}

func TestStandaloneEventEmitterIsOptIn(t *testing.T) {
	var stderr bytes.Buffer
	f := &standaloneFlags{eventWriter: &stderr}
	emitter := newStandaloneEventEmitter(f, "subtitles", "ref.vtt", "ta.srt", "ta.synced.vtt", 1, 1)
	emitter.emit("target_started", nil)
	if stderr.Len() != 0 {
		t.Fatalf("events written without --events-json: %q", stderr.String())
	}
}

func TestVerificationEventIncludesPassGate(t *testing.T) {
	var stderr bytes.Buffer
	f := &standaloneFlags{eventsJSON: true, eventWriter: &stderr}
	emitter := newStandaloneEventEmitter(f, "audio", "ref.m4a", "ta.mka", "ta.synced.mka", 1, 1)
	emitter.emit("verification_complete", func(event *standaloneEvent) {
		setVerificationEventMetrics(event, &standaloneVerification{Passed: true, SyncMS: 8, Score: 9.2})
	})

	payload := strings.TrimSpace(strings.TrimPrefix(stderr.String(), standaloneEventPrefix))
	var got standaloneEvent
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if got.Passed == nil || !*got.Passed {
		t.Fatalf("verification pass gate missing: %+v", got)
	}
	if got.Message != "Verification passed for ta.mka (1/1)" {
		t.Fatalf("verification message = %q", got.Message)
	}
}

func TestSemanticAIEventMakesModelUseExplicit(t *testing.T) {
	var stderr bytes.Buffer
	f := &standaloneFlags{eventsJSON: true, eventWriter: &stderr}
	emitter := newStandaloneEventEmitter(f, "subtitles", "english.vtt", "hindi.srt", "hindi.synced.vtt", 1, 1)
	emitter.emit("semantic_ai_started", func(event *standaloneEvent) {
		event.AI = boolPtr(true)
		event.Model = "gpt-5.4-mini"
	})

	payload := strings.TrimSpace(strings.TrimPrefix(stderr.String(), standaloneEventPrefix))
	var got standaloneEvent
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if got.AI == nil || !*got.AI || got.Model != "gpt-5.4-mini" {
		t.Fatalf("AI identity is not explicit: %+v", got)
	}
	if !strings.Contains(got.Message, "AI fallback started") || !strings.Contains(got.Message, got.Model) || !strings.Contains(got.Message, "no full subtitle translation") {
		t.Fatalf("AI live-log message is incomplete: %q", got.Message)
	}
}
