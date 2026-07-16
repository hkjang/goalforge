package opencode

import (
	"encoding/json"
	"fmt"

	"github.com/goalforge/goalforge/internal/provider"
)

// envelope tolerantly matches opencode's NDJSON run events. The exact event
// vocabulary is not formally specified, so the parser keys off stable
// fields: a type string, sessionID in several casings and nestings, message
// text parts, per-step token usage, and error markers.
type envelope struct {
	Type       string  `json:"type"`
	SessionID  string  `json:"sessionID"`
	SessionAlt string  `json:"session_id"`
	Text       string  `json:"text"`
	Error      *detail `json:"error"`
	Part       *part   `json:"part"`
	Info       *info   `json:"info"`
	Tokens     *tokens `json:"tokens"`
	Usage      *usage  `json:"usage"`
}

type detail struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Data    *struct {
		Message string `json:"message"`
	} `json:"data"`
}

type part struct {
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
	Text      string `json:"text"`
}

type info struct {
	SessionID string  `json:"sessionID"`
	Error     *detail `json:"error"`
	Tokens    *tokens `json:"tokens"`
}

type tokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

type usage struct {
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
}

func (e *envelope) session() string {
	for _, candidate := range []string{e.SessionID, e.SessionAlt} {
		if candidate != "" {
			return candidate
		}
	}
	if e.Part != nil && e.Part.SessionID != "" {
		return e.Part.SessionID
	}
	if e.Info != nil {
		return e.Info.SessionID
	}
	return ""
}

func (e *envelope) errorMessage() string {
	candidates := []*detail{e.Error}
	if e.Info != nil {
		candidates = append(candidates, e.Info.Error)
	}
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		if candidate.Data != nil && candidate.Data.Message != "" {
			return candidate.Data.Message
		}
		if candidate.Message != "" {
			return candidate.Message
		}
		if candidate.Name != "" {
			return candidate.Name
		}
	}
	return ""
}

func DecodeLine(line []byte) ([]provider.Event, error) {
	var e envelope
	if err := json.Unmarshal(line, &e); err != nil {
		return nil, fmt.Errorf("decode opencode event: %w", err)
	}
	event := provider.Event{Raw: append([]byte(nil), line...), SessionID: e.session()}
	if e.Text != "" {
		event.Message = e.Text
	} else if e.Part != nil && e.Part.Text != "" {
		event.Message = e.Part.Text
	}
	failure := e.errorMessage()
	switch {
	case failure != "" || e.Type == "error":
		event.Type = provider.EventFailed
		if event.Message == "" {
			event.Message = failure
		}
	case e.Type == "step_finish" || e.Type == "step-finish":
		event.Type = provider.EventCompleted
	default:
		event.Type = provider.EventMessage
	}
	counted := e.Tokens
	if counted == nil && e.Info != nil {
		counted = e.Info.Tokens
	}
	if counted != nil && (counted.Input > 0 || counted.Output > 0 || counted.Cache.Read > 0 || counted.Cache.Write > 0 || counted.Reasoning > 0) {
		event.Usage = &provider.Usage{InputTokens: counted.Input, OutputTokens: counted.Output, ReasoningTokens: counted.Reasoning, CachedInputTokens: counted.Cache.Read, CacheCreationTokens: counted.Cache.Write}
	} else if e.Usage != nil && (e.Usage.Input > 0 || e.Usage.Output > 0) {
		event.Usage = &provider.Usage{InputTokens: e.Usage.Input, OutputTokens: e.Usage.Output}
	}
	if event.Usage != nil && event.Type == provider.EventMessage {
		event.Type = provider.EventUsage
	}
	return []provider.Event{event}, nil
}
