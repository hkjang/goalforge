package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

type fakeProvider struct {
	startCalls, resumeCalls int
	resumeID                string
	events                  []provider.Event
	startErr                error
	quota                   provider.QuotaSnapshot
	quotaErr                error
	resumeErr               error
	lastRequest             provider.RunRequest
	quotaDelay              time.Duration
}

type controlledProvider struct {
	events     chan provider.Event
	started    chan struct{}
	interrupts int
}

func (f *controlledProvider) Name() string { return "fake" }
func (f *controlledProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true}
}
func (f *controlledProvider) Start(context.Context, provider.RunRequest) (<-chan provider.Event, error) {
	close(f.started)
	f.events <- provider.Event{Type: provider.EventSessionStarted, SessionID: "session-control", Raw: json.RawMessage(`{"type":"session"}`)}
	return f.events, nil
}
func (f *controlledProvider) Resume(ctx context.Context, _ string, request provider.RunRequest) (<-chan provider.Event, error) {
	return f.Start(ctx, request)
}
func (f *controlledProvider) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	return provider.QuotaSnapshot{}, nil
}
func (f *controlledProvider) Interrupt(context.Context, string) error {
	f.interrupts++
	close(f.events)
	return nil
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true}
}
func (f *fakeProvider) Start(_ context.Context, request provider.RunRequest) (<-chan provider.Event, error) {
	f.startCalls++
	f.lastRequest = request
	if f.startErr != nil {
		return nil, f.startErr
	}
	return eventChannel(f.events), nil
}
func (f *fakeProvider) Resume(_ context.Context, id string, request provider.RunRequest) (<-chan provider.Event, error) {
	f.resumeCalls++
	f.resumeID = id
	f.lastRequest = request
	if f.resumeErr != nil {
		return nil, f.resumeErr
	}
	return eventChannel(f.events), nil
}

type namedProvider struct {
	*fakeProvider
	name string
}

func (p *namedProvider) Name() string { return p.name }
func (f *fakeProvider) GetQuota(ctx context.Context, _ provider.AccountRef) (provider.QuotaSnapshot, error) {
	if f.quotaDelay > 0 {
		select {
		case <-ctx.Done():
			return provider.QuotaSnapshot{}, ctx.Err()
		case <-time.After(f.quotaDelay):
		}
	}
	return f.quota, f.quotaErr
}
func (f *fakeProvider) Interrupt(context.Context, string) error { return nil }
func eventChannel(events []provider.Event) <-chan provider.Event {
	ch := make(chan provider.Event, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch
}

func setup(t *testing.T) (context.Context, *store.Store, model.Project) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	p := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "fake", Model: "test"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	return ctx, s, p
}

func TestRunStartsThenResumesPersistedSession(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	fake := &fakeProvider{events: []provider.Event{{Type: provider.EventSessionStarted, SessionID: "session-1", Raw: json.RawMessage(`{"type":"session"}`)}, {Type: provider.EventCompleted, TurnID: "turn-1", Usage: &provider.Usage{InputTokens: 10, OutputTokens: 2}, Raw: json.RawMessage(`{"type":"completed","turn":"1"}`)}}}
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	result, err := o.Run(ctx, Request{RunID: "RUN-1", Prompt: "do one task", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "VERIFYING" || result.Resumed || result.SessionID != "session-1" || result.Usage.InputTokens != 10 {
		t.Fatalf("result=%+v", result)
	}
	if err = s.TransitionProjectState(ctx, project.ID, "VERIFYING", "READY"); err != nil {
		t.Fatal(err)
	}
	fake.events = []provider.Event{{Type: provider.EventCompleted, TurnID: "turn-2", Usage: &provider.Usage{InputTokens: 5}, Raw: json.RawMessage(`{"type":"completed","turn":"2"}`)}}
	result, err = o.Run(ctx, Request{RunID: "RUN-2", Prompt: "continue", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || fake.resumeID != "session-1" || fake.startCalls != 1 || fake.resumeCalls != 1 {
		t.Fatalf("result=%+v fake=%+v", result, fake)
	}
}

func TestProviderTurnTimeoutInterruptsAndReleasesWork(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	goal, err := s.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "WORK-TIMEOUT", GoalID: goal.ID, Type: "IMPLEMENT", Title: "timeout"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNextWorkItem(ctx, goal.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRuntimePolicy(ctx, project.ID, store.RuntimePolicy{TurnTimeout: 20 * time.Millisecond, RunTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	controlled := &controlledProvider{events: make(chan provider.Event, 1), started: make(chan struct{})}
	runner, _ := New(s, controlled)
	result, err := runner.Run(ctx, Request{RunID: "RUN-TIMEOUT", WorkItemID: work.ID, Prompt: "wait forever", Project: project})
	if !errors.Is(err, ErrTurnTimeout) || result.State != "FAILED" || controlled.interrupts != 1 {
		t.Fatalf("result=%+v interrupts=%d err=%v", result, controlled.interrupts, err)
	}
	project, _ = s.ProjectByID(ctx, project.ID)
	items, _ := s.ListWorkItems(ctx, goal.ID)
	if project.State != "FAILED" || len(items) != 1 || items[0].Status != "BACKLOG" {
		t.Fatalf("project=%+v items=%+v", project, items)
	}
}

func TestRunTimeoutDuringPreflightReleasesWork(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	goal, err := s.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "WORK-RUN-TIMEOUT", GoalID: goal.ID, Type: "IMPLEMENT", Title: "run timeout"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNextWorkItem(ctx, goal.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRuntimePolicy(ctx, project.ID, store.RuntimePolicy{TurnTimeout: 20 * time.Millisecond, RunTimeout: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeProvider{quotaDelay: time.Second}
	runner, _ := New(s, fake)
	result, err := runner.Run(ctx, Request{RunID: "RUN-PREFLIGHT-TIMEOUT", WorkItemID: work.ID, Prompt: "work", Project: project})
	if !errors.Is(err, ErrRunTimeout) || result.State != "FAILED" || fake.startCalls != 0 {
		t.Fatalf("result=%+v startCalls=%d err=%v", result, fake.startCalls, err)
	}
	project, _ = s.ProjectByID(ctx, project.ID)
	items, _ := s.ListWorkItems(ctx, goal.ID)
	if project.State != "FAILED" || len(items) != 1 || items[0].Status != "BACKLOG" {
		t.Fatalf("project=%+v items=%+v", project, items)
	}
}

func TestRunReplacesInvalidPersistedSession(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	fake := &fakeProvider{events: []provider.Event{{Type: provider.EventSessionStarted, SessionID: "session-old", Raw: json.RawMessage(`{"type":"session"}`)}, {Type: provider.EventCompleted, TurnID: "turn-old", Raw: json.RawMessage(`{"type":"completed"}`)}}}
	o, _ := New(s, fake)
	if _, err := o.Run(ctx, Request{RunID: "RUN-OLD", Prompt: "first", Project: project}); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionProjectState(ctx, project.ID, "VERIFYING", "READY"); err != nil {
		t.Fatal(err)
	}
	fake.resumeErr = provider.ErrSessionInvalid
	fake.events = []provider.Event{{Type: provider.EventSessionStarted, SessionID: "session-new", Raw: json.RawMessage(`{"type":"session"}`)}, {Type: provider.EventCompleted, TurnID: "turn-new", Raw: json.RawMessage(`{"type":"completed"}`)}}
	result, err := o.Run(ctx, Request{RunID: "RUN-NEW", Prompt: "continue", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed || result.SessionID != "session-new" || fake.resumeCalls != 1 || fake.startCalls != 2 {
		t.Fatalf("result=%+v fake=%+v", result, fake)
	}
	sessions, err := s.ListSessions(ctx, project.ID)
	if err != nil || len(sessions) != 2 || sessions[0].Status != "ACTIVE" || sessions[1].Status != "INVALID" {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
}

func TestProviderSwitchStartsNewSessionWithNeutralHandoff(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	goal, err := s.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "WORK-H", GoalID: goal.ID, Type: "IMPLEMENT", Title: "handoff work"}); err != nil {
		t.Fatal(err)
	}
	old := &fakeProvider{events: []provider.Event{{Type: provider.EventSessionStarted, SessionID: "secret-old-session", Raw: json.RawMessage(`{"type":"session"}`)}, {Type: provider.EventCompleted, TurnID: "old-turn", Raw: json.RawMessage(`{"type":"completed"}`)}}}
	oldRunner, _ := New(s, old)
	if _, err = oldRunner.Run(ctx, Request{RunID: "RUN-H-OLD", Prompt: "old work", Project: project}); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionProjectState(ctx, project.ID, "VERIFYING", "READY"); err != nil {
		t.Fatal(err)
	}
	handoff, err := s.SwitchProvider(ctx, project.ID, "new-provider", "new-model", "quality comparison")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(handoff.ContentJSON, "secret-old-session") {
		t.Fatal("handoff leaked old provider session ID")
	}
	project, _ = s.ProjectByID(ctx, project.ID)
	newFake := &fakeProvider{events: []provider.Event{{Type: provider.EventSessionStarted, SessionID: "new-session", Raw: json.RawMessage(`{"type":"session"}`)}, {Type: provider.EventCompleted, TurnID: "new-turn", Raw: json.RawMessage(`{"type":"completed"}`)}}}
	newRunner, _ := New(s, &namedProvider{fakeProvider: newFake, name: "new-provider"})
	result, err := newRunner.Run(ctx, Request{RunID: "RUN-H-NEW", Prompt: "continue work", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed || newFake.startCalls != 1 || newFake.resumeCalls != 0 || !strings.Contains(newFake.lastRequest.Prompt, "handoff work") || strings.Contains(newFake.lastRequest.Prompt, "secret-old-session") {
		t.Fatalf("result=%+v provider=%+v prompt=%q", result, newFake, newFake.lastRequest.Prompt)
	}
	if _, err = s.PendingHandoff(ctx, project.ID, "new-provider"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("handoff was not consumed: %v", err)
	}
	sessions, err := s.ListSessions(ctx, project.ID)
	handoffSessions := 0
	for _, session := range sessions {
		if session.Status == "HANDOFF" {
			handoffSessions++
		}
	}
	if err != nil || len(sessions) != 2 || handoffSessions != 1 {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
}

func TestRunFailsWithoutCompletionEvent(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	fake := &fakeProvider{events: []provider.Event{{Type: provider.EventMessage, Raw: json.RawMessage(`{"type":"message"}`)}}}
	o, _ := New(s, fake)
	result, err := o.Run(ctx, Request{RunID: "RUN-1", Prompt: "task", Project: project})
	if err == nil || result.State != "FAILED" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err = s.TransitionProjectState(ctx, project.ID, "FAILED", "READY"); err != nil {
		t.Fatalf("project was not failed: %v", err)
	}
}

func TestRunPersistsSyntheticFailureEvent(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	fake := &fakeProvider{events: []provider.Event{{Type: provider.EventFailed, Err: errors.New("boom")}}}
	o, _ := New(s, fake)
	if _, err := o.Run(ctx, Request{RunID: "RUN-1", Prompt: "task", Project: project}); err == nil {
		t.Fatal("expected failure")
	}
	usage, err := s.RunUsage(ctx, "RUN-1")
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 0 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestPauseRequestInterruptsAndCheckpointsRun(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	if _, err := s.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}}); err != nil {
		t.Fatal(err)
	}
	fake := &controlledProvider{events: make(chan provider.Event, 1), started: make(chan struct{})}
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	if err = o.ConfigureControl(fakeInspector{snapshot: gitops.Snapshot{CommitSHA: "abc", Branch: "main", DirtyFiles: []string{"work.go"}}}, 5*time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, runErr := o.Run(ctx, Request{RunID: "RUN-PAUSE", WorkItemID: "WORK-1", Prompt: "work", Project: project, WorkspaceWrite: true})
		done <- outcome{result: result, err: runErr}
	}()
	<-fake.started
	control, err := s.RequestRunControl(ctx, project.ID, "PAUSE")
	if err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.result.State != "BLOCKED" || fake.interrupts != 1 {
		t.Fatalf("result=%+v interrupts=%d err=%v", got.result, fake.interrupts, got.err)
	}
	cp, err := s.LatestCheckpoint(ctx, project.ID)
	if err != nil || cp.RunID != "RUN-PAUSE" || cp.SessionID != "session-control" || cp.NextAction == "" {
		t.Fatalf("checkpoint=%+v err=%v", cp, err)
	}
	if _, err = s.PendingRunControl(ctx, "RUN-PAUSE"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("control %s still pending: %v", control.ID, err)
	}
}
