package orchestrator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// deadSessionProvider stands in for a CLI that no longer has the session
// GoalForge is still holding on to: the resume starts fine and the refusal
// arrives in the stream, which is exactly why the existing recovery path never
// ran.
type deadSessionProvider struct {
	resumeCalls, startCalls int
	resumedWith             string
}

func (p *deadSessionProvider) Name() string { return "fake" }
func (p *deadSessionProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true}
}

func (p *deadSessionProvider) Start(_ context.Context, _ provider.RunRequest) (<-chan provider.Event, error) {
	p.startCalls++
	events := make(chan provider.Event, 2)
	events <- provider.Event{Type: provider.EventSessionStarted, SessionID: "session-fresh", Raw: json.RawMessage(`{"t":"s"}`)}
	events <- provider.Event{Type: provider.EventCompleted, TurnID: "t1", Raw: json.RawMessage(`{"t":"c"}`)}
	close(events)
	return events, nil
}

func (p *deadSessionProvider) Resume(_ context.Context, id string, _ provider.RunRequest) (<-chan provider.Event, error) {
	p.resumeCalls++
	p.resumedWith = id
	events := make(chan provider.Event, 1)
	// Resume itself succeeds; the CLI reports the missing session afterwards.
	events <- provider.Event{Type: provider.EventFailed,
		Message: "No saved session found with ID " + id, Raw: json.RawMessage(`{"t":"f"}`)}
	close(events)
	return events, nil
}

func (p *deadSessionProvider) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	return provider.QuotaSnapshot{}, nil
}
func (p *deadSessionProvider) Interrupt(context.Context, string) error { return nil }

func seedActiveSession(t *testing.T, ctx context.Context, s *store.Store, projectID, sessionID string) {
	t.Helper()
	if err := s.SeedActiveSession(ctx, projectID, "fake", sessionID, "test"); err != nil {
		t.Fatal(err)
	}
}

// The failure this reproduces came from a real worker run: the session was
// ACTIVE in GoalForge and gone from the CLI, the resume failed, the retry
// failed the same way, and the project went to FAILED needing a person.
func TestDeadSessionIsDroppedAndTheTurnStartsFresh(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	seedActiveSession(t, ctx, s, project.ID, "32e113ab-8c5e-43be-a687-f780f6522654")
	fake := &deadSessionProvider{}
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	result, err := o.Run(ctx, Request{RunID: "RUN-1", Prompt: "go", Project: project, TaskType: "IMPLEMENT_SELECTED"})
	if err != nil {
		t.Fatalf("a dead session must not fail the run: %v", err)
	}
	if fake.resumeCalls != 1 || fake.resumedWith != "32e113ab-8c5e-43be-a687-f780f6522654" {
		t.Fatalf("the stored binding must be tried once: calls=%d id=%q", fake.resumeCalls, fake.resumedWith)
	}
	if fake.startCalls != 1 {
		t.Fatalf("the turn must be done again from a fresh session: startCalls=%d", fake.startCalls)
	}
	// A write turn hands off to verification, which is what a turn that did
	// not fail looks like.
	if result.State != "VERIFYING" {
		t.Fatalf("the recovered turn must proceed to verification: %+v", result)
	}
	if result.Resumed || result.SessionID != "session-fresh" {
		t.Fatalf("the retry runs on a new session, not the dead one: %+v", result)
	}
	// The dead binding has to be gone, or the next run reaches for it again
	// and the recovery works exactly once. What is active now is the fresh
	// session the retry produced.
	active, sessionErr := s.ActiveSession(ctx, project.ID, "fake")
	if sessionErr != nil {
		t.Fatalf("the fresh session must be recorded: %v", sessionErr)
	}
	if active.SessionID == "32e113ab-8c5e-43be-a687-f780f6522654" {
		t.Fatal("the dead binding is still active; the next run would fail the same way")
	}
}

// A resume that failed for any other reason must still fail. Starting fresh
// would throw away the conversation over a transient outage and hide the real
// fault behind a retry that looks fine.
func TestOtherResumeFailuresStillFail(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	seedActiveSession(t, ctx, s, project.ID, "session-live")
	fake := &quotaFailingProvider{}
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Run(ctx, Request{RunID: "RUN-1", Prompt: "go", Project: project, TaskType: "IMPLEMENT_SELECTED"}); err == nil {
		t.Fatal("a quota failure is not a dead session and must surface")
	}
	if fake.startCalls != 0 {
		t.Fatalf("no fresh session may be started for an unrelated failure: %d", fake.startCalls)
	}
	// The binding survives, because nothing said it was gone.
	if _, sessionErr := s.ActiveSession(ctx, project.ID, "fake"); sessionErr != nil {
		t.Fatalf("a live session must not be invalidated: %v", sessionErr)
	}
}

type quotaFailingProvider struct{ startCalls int }

func (p *quotaFailingProvider) Name() string { return "fake" }
func (p *quotaFailingProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true}
}
func (p *quotaFailingProvider) Start(context.Context, provider.RunRequest) (<-chan provider.Event, error) {
	p.startCalls++
	events := make(chan provider.Event, 1)
	events <- provider.Event{Type: provider.EventCompleted, Raw: json.RawMessage(`{"t":"c"}`)}
	close(events)
	return events, nil
}
func (p *quotaFailingProvider) Resume(context.Context, string, provider.RunRequest) (<-chan provider.Event, error) {
	events := make(chan provider.Event, 1)
	events <- provider.Event{Type: provider.EventFailed, Message: "quota exceeded", Raw: json.RawMessage(`{"t":"f"}`)}
	close(events)
	return events, nil
}
func (p *quotaFailingProvider) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	return provider.QuotaSnapshot{}, nil
}
func (p *quotaFailingProvider) Interrupt(context.Context, string) error { return nil }

// The retry starts fresh, so it cannot fail the same way — but if the fresh
// session somehow reports the same thing, the run must stop rather than loop.
func TestRecoveryIsNotRetriedForever(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	seedActiveSession(t, ctx, s, project.ID, "session-dead")
	fake := &alwaysDeadProvider{}
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Run(ctx, Request{RunID: "RUN-1", Prompt: "go", Project: project, TaskType: "IMPLEMENT_SELECTED"}); err == nil {
		t.Fatal("a fresh session that still fails must surface")
	}
	// One resume, one fresh start, and then it stops: the retry cannot fail
	// the same way, so a second recovery would be a loop.
	if fake.resumeCalls != 1 || fake.startCalls != 1 {
		t.Fatalf("the recovery is bounded to one retry: resume=%d start=%d", fake.resumeCalls, fake.startCalls)
	}
}

type alwaysDeadProvider struct{ startCalls, resumeCalls int }

func (p *alwaysDeadProvider) Name() string { return "fake" }
func (p *alwaysDeadProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true}
}
func (p *alwaysDeadProvider) Start(context.Context, provider.RunRequest) (<-chan provider.Event, error) {
	p.startCalls++
	events := make(chan provider.Event, 1)
	events <- provider.Event{Type: provider.EventFailed, Message: "No saved session found with ID x", Raw: json.RawMessage(`{"t":"f"}`)}
	close(events)
	return events, nil
}
func (p *alwaysDeadProvider) Resume(context.Context, string, provider.RunRequest) (<-chan provider.Event, error) {
	p.resumeCalls++
	events := make(chan provider.Event, 1)
	events <- provider.Event{Type: provider.EventFailed, Message: "No saved session found with ID x", Raw: json.RawMessage(`{"t":"f"}`)}
	close(events)
	return events, nil
}
func (p *alwaysDeadProvider) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	return provider.QuotaSnapshot{}, nil
}
func (p *alwaysDeadProvider) Interrupt(context.Context, string) error { return nil }
