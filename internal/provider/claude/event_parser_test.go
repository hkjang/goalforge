package claude

import (
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
)

func TestDecodeClaudeEvents(t *testing.T) {
	events, err := DecodeLine([]byte(`{"type":"system","subtype":"init","session_id":"ses_123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Type != provider.EventSessionStarted || events[0].SessionID != "ses_123" {
		t.Fatalf("event=%+v", events[0])
	}
	events, err = DecodeLine([]byte(`{"type":"result","subtype":"success","session_id":"ses_123","result":"done","total_cost_usd":0.012,"usage":{"input_tokens":80,"output_tokens":15,"cache_read_input_tokens":30,"cache_creation_input_tokens":4}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := events[0]
	if got.Type != provider.EventCompleted || got.Usage == nil || got.Usage.CachedInputTokens != 30 || got.Usage.CacheCreationTokens != 4 || got.Usage.CostUSD != 0.012 {
		t.Fatalf("event=%+v", got)
	}
}

func TestDecodeClaudeNonSuccessResultFails(t *testing.T) {
	input := `{"type":"result","subtype":"error_max_budget_usd","is_error":true,"session_id":"ses","errors":["budget exceeded"]}`
	events, err := DecodeLine([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Type != provider.EventFailed || events[0].Message != "budget exceeded" {
		t.Fatalf("event=%+v", events[0])
	}
}
