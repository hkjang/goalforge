package codex

import (
	"encoding/json"
	"fmt"

	"github.com/goalforge/goalforge/internal/provider"
)

type envelope struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
	Message  string `json:"message"`
	Usage    *struct {
		Input     int64 `json:"input_tokens"`
		Output    int64 `json:"output_tokens"`
		Cached    int64 `json:"cached_input_tokens"`
		Reasoning int64 `json:"reasoning_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
	Item  *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}

func DecodeLine(line []byte) ([]provider.Event, error) {
	var e envelope
	if err := json.Unmarshal(line, &e); err != nil {
		return nil, fmt.Errorf("decode Codex event: %w", err)
	}
	event := provider.Event{Raw: append([]byte(nil), line...), SessionID: e.ThreadID, TurnID: e.TurnID, Message: e.Message}
	if e.Item != nil && e.Item.Type == "agent_message" {
		event.Message = e.Item.Text
	}
	switch e.Type {
	case "thread.started":
		event.Type = provider.EventSessionStarted
	case "turn.started":
		event.Type = provider.EventTurnStarted
	case "turn.completed":
		event.Type = provider.EventCompleted
	case "turn.failed", "error":
		event.Type = provider.EventFailed
	default:
		event.Type = provider.EventMessage
	}
	if e.Usage != nil {
		event.Usage = &provider.Usage{InputTokens: e.Usage.Input, OutputTokens: e.Usage.Output, CachedInputTokens: e.Usage.Cached, ReasoningTokens: e.Usage.Reasoning}
		if event.Type == provider.EventMessage {
			event.Type = provider.EventUsage
		}
	}
	return []provider.Event{event}, nil
}
