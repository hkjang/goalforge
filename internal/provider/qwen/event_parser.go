package qwen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goalforge/goalforge/internal/provider"
)

// envelope matches Qwen Code's Claude-compatible stream-json events:
// {"type":"system","subtype":"session_start","session_id":...},
// {"type":"assistant","message":{...}}, and
// {"type":"result","subtype":"success","is_error":false,"result":...,
//
//	"usage":{...},"duration_ms":...}.
type envelope struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Message   *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Usage *struct {
		Input       int64 `json:"input_tokens"`
		Output      int64 `json:"output_tokens"`
		CacheRead   int64 `json:"cache_read_input_tokens"`
		CacheCreate int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// DecodeLine decodes one stream-json line. It also tolerates the buffered
// --output-format json shape, where every message arrives as one JSON array.
func DecodeLine(line []byte) ([]provider.Event, error) {
	trimmed := strings.TrimSpace(string(line))
	if strings.HasPrefix(trimmed, "[") {
		var batch []json.RawMessage
		if err := json.Unmarshal(line, &batch); err != nil {
			return nil, fmt.Errorf("decode Qwen Code event array: %w", err)
		}
		var events []provider.Event
		for _, raw := range batch {
			decoded, err := decodeOne(raw)
			if err != nil {
				return nil, err
			}
			events = append(events, decoded)
		}
		return events, nil
	}
	event, err := decodeOne(line)
	if err != nil {
		return nil, err
	}
	return []provider.Event{event}, nil
}

func decodeOne(line []byte) (provider.Event, error) {
	var e envelope
	if err := json.Unmarshal(line, &e); err != nil {
		return provider.Event{}, fmt.Errorf("decode Qwen Code event: %w", err)
	}
	event := provider.Event{Raw: append([]byte(nil), line...), SessionID: e.SessionID, Message: e.Result}
	if event.Message == "" && e.Message != nil {
		var parts []string
		for _, content := range e.Message.Content {
			if content.Type == "text" && content.Text != "" {
				parts = append(parts, content.Text)
			}
		}
		event.Message = strings.Join(parts, "\n")
	}
	switch {
	case e.Type == "system" && (e.Subtype == "session_start" || e.Subtype == "init"):
		event.Type = provider.EventSessionStarted
	case e.Type == "result" && !e.IsError:
		event.Type = provider.EventCompleted
	case e.Type == "result":
		event.Type = provider.EventFailed
	default:
		event.Type = provider.EventMessage
	}
	if e.Usage != nil && (e.Usage.Input > 0 || e.Usage.Output > 0 || e.Usage.CacheRead > 0 || e.Usage.CacheCreate > 0) {
		event.Usage = &provider.Usage{InputTokens: e.Usage.Input, OutputTokens: e.Usage.Output, CachedInputTokens: e.Usage.CacheRead, CacheCreationTokens: e.Usage.CacheCreate}
		if event.Type == provider.EventMessage {
			event.Type = provider.EventUsage
		}
	}
	return event, nil
}
