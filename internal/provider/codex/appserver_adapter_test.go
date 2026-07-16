package codex

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/provider"
)

type fakeRPC struct {
	mu            sync.Mutex
	calls         []string
	params        map[string][]json.RawMessage
	notifications chan RPCNotification
	quota         any
}

func newFakeRPC() *fakeRPC {
	return &fakeRPC{params: make(map[string][]json.RawMessage), notifications: make(chan RPCNotification, 16)}
}
func (f *fakeRPC) Call(_ context.Context, method string, params, result any) error {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	f.calls = append(f.calls, method)
	f.params[method] = append(f.params[method], raw)
	f.mu.Unlock()
	var response any
	switch method {
	case "thread/start", "thread/resume":
		response = map[string]any{"thread": map[string]string{"id": "thr-1"}}
	case "turn/start":
		response = map[string]any{"turn": map[string]string{"id": "turn-1"}}
	case "account/rateLimits/read":
		response = f.quota
	default:
		response = map[string]any{}
	}
	if result != nil {
		encoded, _ := json.Marshal(response)
		_ = json.Unmarshal(encoded, result)
	}
	return nil
}
func (f *fakeRPC) Notify(context.Context, string, any) error { return nil }
func (f *fakeRPC) Notifications() <-chan RPCNotification     { return f.notifications }
func (f *fakeRPC) Close() error                              { close(f.notifications); return nil }
func notification(method string, params any) RPCNotification {
	rawParams, _ := json.Marshal(params)
	raw, _ := json.Marshal(map[string]any{"method": method, "params": params})
	return RPCNotification{Method: method, Params: rawParams, Raw: raw}
}

func TestAppServerAdapterStartsGoalStreamsUsageAndMessage(t *testing.T) {
	rpc := newFakeRPC()
	adapter, err := NewAppServerAdapter(rpc)
	if err != nil {
		t.Fatal(err)
	}
	budget := int64(1000)
	events, err := adapter.Start(context.Background(), provider.RunRequest{RunID: "R1", Prompt: "implement", WorkDir: "/repo", Model: "test", WorkspaceWrite: true, GoalObjective: "ship", GoalTokenBudget: &budget, OutputSchema: `{"type":"object"}`})
	if err != nil {
		t.Fatal(err)
	}
	rpc.notifications <- notification("thread/tokenUsage/updated", map[string]any{"threadId": "thr-1", "turnId": "turn-1", "tokenUsage": map[string]any{"last": map[string]int64{"inputTokens": 10, "cachedInputTokens": 4, "outputTokens": 3, "reasoningOutputTokens": 2}}})
	rpc.notifications <- notification("item/completed", map[string]any{"threadId": "thr-1", "turnId": "turn-1", "item": map[string]string{"type": "agentMessage", "text": `{"status":"completed"}`}})
	rpc.notifications <- notification("turn/completed", map[string]any{"threadId": "thr-1", "turnId": "turn-1", "turn": map[string]string{"status": "completed"}})
	var got []provider.Event
	for event := range events {
		got = append(got, event)
	}
	if len(got) != 3 || got[0].SessionID != "thr-1" || got[1].Message != `{"status":"completed"}` || got[2].Type != provider.EventCompleted || got[2].Usage == nil || got[2].Usage.CachedInputTokens != 4 {
		t.Fatalf("events=%+v", got)
	}
	rpc.mu.Lock()
	calls := append([]string(nil), rpc.calls...)
	turnParams := rpc.params["turn/start"][0]
	rpc.mu.Unlock()
	if len(calls) != 3 || calls[0] != "thread/start" || calls[1] != "thread/goal/set" || calls[2] != "turn/start" {
		t.Fatalf("calls=%v", calls)
	}
	var params map[string]any
	_ = json.Unmarshal(turnParams, &params)
	if params["outputSchema"] == nil || params["sandboxPolicy"] == nil {
		t.Fatalf("turn params=%v", params)
	}
}

func TestAppServerAdapterReadsExactQuotaReset(t *testing.T) {
	reset := time.Date(2026, 7, 16, 15, 0, 0, 0, time.UTC)
	rpc := newFakeRPC()
	rpc.quota = map[string]any{"rateLimits": map[string]any{"limitId": "codex", "primary": map[string]any{"usedPercent": 90, "resetsAt": reset.Unix()}, "secondary": map[string]any{"usedPercent": 100, "resetsAt": reset.Add(24 * time.Hour).Unix()}}}
	adapter, _ := NewAppServerAdapter(rpc)
	quota, err := adapter.GetQuota(context.Background(), provider.AccountRef{})
	if err != nil {
		t.Fatal(err)
	}
	if quota.UsedPercent != 100 || !quota.LimitReached || quota.ResetAt == nil || !quota.ResetAt.Equal(reset.Add(24*time.Hour)) || quota.Source != "app_server" || quota.LimitType != "codex:secondary" {
		t.Fatalf("quota=%+v", quota)
	}
}
