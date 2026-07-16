package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goalforge/goalforge/internal/provider"
)

type envelope struct {
	Type      string   `json:"type"`
	Subtype   string   `json:"subtype"`
	SessionID string   `json:"session_id"`
	Result    string   `json:"result"`
	TotalCost float64  `json:"total_cost_usd"`
	IsError   bool     `json:"is_error"`
	Errors    []string `json:"errors"`
	Usage     *struct {
		Input          int64 `json:"input_tokens"`
		Output         int64 `json:"output_tokens"`
		CacheRead      int64 `json:"cache_read_input_tokens"`
		CacheReadAlt   int64 `json:"cache_read_tokens"`
		CacheCreate    int64 `json:"cache_creation_input_tokens"`
		CacheCreateAlt int64 `json:"cache_creation_tokens"`
	} `json:"usage"`
}

func DecodeLine(line []byte) ([]provider.Event, error) {
	var e envelope
	if err := json.Unmarshal(line, &e); err != nil {
		return nil, fmt.Errorf("decode Claude event: %w", err)
	}
	event := provider.Event{Raw: append([]byte(nil), line...), SessionID: e.SessionID, Message: e.Result}
	if event.Message == "" && len(e.Errors) > 0 {
		event.Message = strings.Join(e.Errors, "; ")
	}
	switch {
	case e.Type == "system" && e.Subtype == "init":
		event.Type = provider.EventSessionStarted
	case e.Type == "result" && !e.IsError && e.Subtype == "success":
		event.Type = provider.EventCompleted
	case e.Type == "result":
		event.Type = provider.EventFailed
	default:
		event.Type = provider.EventMessage
	}
	if e.Usage != nil {
		cached := e.Usage.CacheRead
		if cached == 0 {
			cached = e.Usage.CacheReadAlt
		}
		created := e.Usage.CacheCreate
		if created == 0 {
			created = e.Usage.CacheCreateAlt
		}
		event.Usage = &provider.Usage{InputTokens: e.Usage.Input, OutputTokens: e.Usage.Output, CachedInputTokens: cached, CacheCreationTokens: created, CostUSD: e.TotalCost}
		if event.Type == provider.EventMessage {
			event.Type = provider.EventUsage
		}
	}
	return []provider.Event{event}, nil
}
