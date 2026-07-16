package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goalforge/goalforge/internal/provider"
)

type AppServerAdapter struct {
	rpc      AppServerRPC
	turnLock sync.Mutex
	runsMu   sync.Mutex
	runs     map[string]activeTurn
}

type activeTurn struct{ ThreadID, TurnID string }

func NewAppServerAdapter(rpc AppServerRPC) (*AppServerAdapter, error) {
	if rpc == nil {
		return nil, errors.New("Codex app-server RPC client is required")
	}
	return &AppServerAdapter{rpc: rpc, runs: make(map[string]activeTurn)}, nil
}

func StartAppServerAdapter(ctx context.Context, binary string) (*AppServerAdapter, error) {
	client, err := StartAppServer(ctx, binary)
	if err != nil {
		return nil, err
	}
	adapter, err := NewAppServerAdapter(client)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return adapter, nil
}

func (a *AppServerAdapter) Close() error { return a.rpc.Close() }

func (a *AppServerAdapter) Name() string { return "codex" }
func (a *AppServerAdapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true, ExactQuotaResetAt: true, RealtimeTokenUsage: true, NativeGoal: true, NativeTokenBudget: true}
}

func (a *AppServerAdapter) Start(ctx context.Context, request provider.RunRequest) (<-chan provider.Event, error) {
	params := map[string]any{"cwd": request.WorkDir, "sandbox": sandboxName(request.WorkspaceWrite), "approvalPolicy": "never", "ephemeral": request.Ephemeral}
	if request.Model != "" {
		params["model"] = request.Model
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := a.rpc.Call(ctx, "thread/start", params, &response); err != nil {
		return nil, err
	}
	if response.Thread.ID == "" {
		return nil, errors.New("Codex app-server thread/start returned no thread ID")
	}
	return a.startTurn(ctx, response.Thread.ID, request)
}

func (a *AppServerAdapter) Resume(ctx context.Context, sessionID string, request provider.RunRequest) (<-chan provider.Event, error) {
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}
	params := map[string]any{"threadId": sessionID, "cwd": request.WorkDir, "sandbox": sandboxName(request.WorkspaceWrite), "approvalPolicy": "never"}
	if request.Model != "" {
		params["model"] = request.Model
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := a.rpc.Call(ctx, "thread/resume", params, &response); err != nil {
		return nil, err
	}
	if response.Thread.ID != "" && response.Thread.ID != sessionID {
		return nil, fmt.Errorf("Codex app-server resumed unexpected thread %s", response.Thread.ID)
	}
	return a.startTurn(ctx, sessionID, request)
}

func (a *AppServerAdapter) startTurn(ctx context.Context, threadID string, request provider.RunRequest) (<-chan provider.Event, error) {
	a.turnLock.Lock()
	failed := true
	defer func() {
		if failed {
			a.turnLock.Unlock()
		}
	}()
	if request.GoalObjective != "" || request.GoalTokenBudget != nil {
		goal := map[string]any{"threadId": threadID, "status": "active"}
		if request.GoalObjective != "" {
			goal["objective"] = request.GoalObjective
		}
		if request.GoalTokenBudget != nil {
			goal["tokenBudget"] = *request.GoalTokenBudget
		}
		if err := a.rpc.Call(ctx, "thread/goal/set", goal, nil); err != nil {
			return nil, err
		}
	}
	params := map[string]any{"threadId": threadID, "input": []map[string]any{{"type": "text", "text": request.Prompt}}, "cwd": request.WorkDir}
	if request.Model != "" {
		params["model"] = request.Model
	}
	if request.OutputSchema != "" {
		var schema any
		if err := json.Unmarshal([]byte(request.OutputSchema), &schema); err != nil {
			return nil, fmt.Errorf("decode output schema: %w", err)
		}
		params["outputSchema"] = schema
	}
	params["sandboxPolicy"] = sandboxPolicy(request.WorkspaceWrite, request.WorkDir)
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := a.rpc.Call(ctx, "turn/start", params, &response); err != nil {
		return nil, err
	}
	if response.Turn.ID == "" {
		return nil, errors.New("Codex app-server turn/start returned no turn ID")
	}
	a.runsMu.Lock()
	a.runs[request.RunID] = activeTurn{ThreadID: threadID, TurnID: response.Turn.ID}
	a.runsMu.Unlock()
	out := make(chan provider.Event, 16)
	failed = false
	go a.streamTurn(ctx, request.RunID, threadID, response.Turn.ID, out)
	return out, nil
}

func (a *AppServerAdapter) streamTurn(ctx context.Context, runID, threadID, turnID string, out chan<- provider.Event) {
	defer close(out)
	defer a.turnLock.Unlock()
	defer func() { a.runsMu.Lock(); delete(a.runs, runID); a.runsMu.Unlock() }()
	out <- provider.Event{Type: provider.EventSessionStarted, RunID: runID, SessionID: threadID, TurnID: turnID, Raw: syntheticRaw("thread/started", threadID, turnID)}
	var usage *provider.Usage
	for {
		select {
		case <-ctx.Done():
			_ = a.Interrupt(context.Background(), runID)
			out <- provider.Event{Type: provider.EventFailed, RunID: runID, SessionID: threadID, TurnID: turnID, Err: ctx.Err()}
			return
		case notification, ok := <-a.rpc.Notifications():
			if !ok {
				out <- provider.Event{Type: provider.EventFailed, RunID: runID, SessionID: threadID, TurnID: turnID, Err: errors.New("Codex app-server notification stream closed")}
				return
			}
			var base struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
			}
			if json.Unmarshal(notification.Params, &base) != nil || base.ThreadID != threadID || (base.TurnID != "" && base.TurnID != turnID) {
				continue
			}
			switch notification.Method {
			case "thread/tokenUsage/updated":
				usage = decodeAppServerUsage(notification.Params)
			case "item/completed":
				if message := decodeAgentMessage(notification.Params); message != "" {
					out <- provider.Event{Type: provider.EventMessage, RunID: runID, SessionID: threadID, TurnID: turnID, Message: message, Raw: notification.Raw}
				}
			case "turn/completed":
				status := decodeTurnStatus(notification.Params)
				typeName := provider.EventCompleted
				if status != "completed" {
					typeName = provider.EventFailed
				}
				out <- provider.Event{Type: typeName, RunID: runID, SessionID: threadID, TurnID: turnID, Message: status, Usage: usage, Raw: notification.Raw}
				return
			}
		}
	}
}

func (a *AppServerAdapter) GetQuota(ctx context.Context, _ provider.AccountRef) (provider.QuotaSnapshot, error) {
	var response struct {
		RateLimits rateLimitSnapshot `json:"rateLimits"`
	}
	if err := a.rpc.Call(ctx, "account/rateLimits/read", map[string]any{}, &response); err != nil {
		return provider.QuotaSnapshot{Provider: a.Name()}, err
	}
	return response.RateLimits.toQuota(a.Name()), nil
}

func (a *AppServerAdapter) Interrupt(ctx context.Context, runID string) error {
	a.runsMu.Lock()
	active, ok := a.runs[runID]
	a.runsMu.Unlock()
	if !ok {
		return fmt.Errorf("run %s is not active", runID)
	}
	return a.rpc.Call(ctx, "turn/interrupt", map[string]string{"threadId": active.ThreadID, "turnId": active.TurnID}, nil)
}

type rateLimitWindow struct {
	UsedPercent int    `json:"usedPercent"`
	ResetsAt    *int64 `json:"resetsAt"`
}
type rateLimitSnapshot struct {
	LimitID              string           `json:"limitId"`
	RateLimitReachedType string           `json:"rateLimitReachedType"`
	Primary              *rateLimitWindow `json:"primary"`
	Secondary            *rateLimitWindow `json:"secondary"`
}

func (r rateLimitSnapshot) toQuota(providerName string) provider.QuotaSnapshot {
	window, kind := r.Primary, "primary"
	if r.Secondary != nil && (window == nil || r.Secondary.UsedPercent > window.UsedPercent) {
		window, kind = r.Secondary, "secondary"
	}
	result := provider.QuotaSnapshot{Provider: providerName, LimitType: kind, Source: "app_server", Confidence: "high", LimitReached: r.RateLimitReachedType != "", RawMessage: r.RateLimitReachedType}
	if r.LimitID != "" {
		result.LimitType = r.LimitID + ":" + kind
	}
	if window != nil {
		result.UsedPercent = float64(window.UsedPercent)
		result.LimitReached = result.LimitReached || window.UsedPercent >= 100
		if window.ResetsAt != nil {
			value := time.Unix(*window.ResetsAt, 0).UTC()
			result.ResetAt = &value
		}
	}
	return result
}

func sandboxName(write bool) string {
	if write {
		return "workspace-write"
	}
	return "read-only"
}
func sandboxPolicy(write bool, root string) map[string]any {
	if write {
		return map[string]any{"type": "workspaceWrite", "writableRoots": []string{root}, "networkAccess": false}
	}
	return map[string]any{"type": "readOnly", "networkAccess": false}
}
func syntheticRaw(method, threadID, turnID string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"method": method, "params": map[string]string{"threadId": threadID, "turnId": turnID}})
	return raw
}
func decodeAgentMessage(raw json.RawMessage) string {
	var p struct {
		Item struct{ Type, Text string } `json:"item"`
	}
	_ = json.Unmarshal(raw, &p)
	if p.Item.Type == "agentMessage" {
		return p.Item.Text
	}
	return ""
}
func decodeTurnStatus(raw json.RawMessage) string {
	var p struct {
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Turn.Status
}
func decodeAppServerUsage(raw json.RawMessage) *provider.Usage {
	var payload struct {
		TokenUsage struct {
			Last struct {
				Input     int64 `json:"inputTokens"`
				Cached    int64 `json:"cachedInputTokens"`
				Output    int64 `json:"outputTokens"`
				Reasoning int64 `json:"reasoningOutputTokens"`
			} `json:"last"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	return &provider.Usage{InputTokens: payload.TokenUsage.Last.Input, CachedInputTokens: payload.TokenUsage.Last.Cached, OutputTokens: payload.TokenUsage.Last.Output, ReasoningTokens: payload.TokenUsage.Last.Reasoning}
}
