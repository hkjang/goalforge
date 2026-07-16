package codex

import (
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
)

func TestDecodeCodexEvents(t *testing.T) {
	events, err := DecodeLine([]byte(`{"type":"thread.started","thread_id":"thr_123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Type != provider.EventSessionStarted || events[0].SessionID != "thr_123" {
		t.Fatalf("event=%+v", events[0])
	}
	events, err = DecodeLine([]byte(`{"type":"turn.completed","turn_id":"turn_1","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_tokens":5}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := events[0]
	if got.Type != provider.EventCompleted || got.Usage == nil || got.Usage.InputTokens != 100 || got.Usage.CachedInputTokens != 40 || got.Usage.ReasoningTokens != 5 {
		t.Fatalf("event=%+v", got)
	}
}

func TestDecodeCodexInvalidJSON(t *testing.T) {
	if _, err := DecodeLine([]byte(`nope`)); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestDecodeCodexAgentMessage(t *testing.T) {
	events, err := DecodeLine([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"{\"ideas\":[]}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Type != provider.EventMessage || events[0].Message != `{"ideas":[]}` {
		t.Fatalf("event=%+v", events[0])
	}
}
