package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func completedProvider() *fakeProvider {
	return &fakeProvider{events: []provider.Event{
		{Type: provider.EventSessionStarted, SessionID: "s1", Raw: json.RawMessage(`{"type":"session"}`)},
		{Type: provider.EventCompleted, TurnID: "t1", Usage: &provider.Usage{InputTokens: 1},
			Raw: json.RawMessage(`{"type":"completed"}`)},
	}}
}

// A session that cannot run the project's tests writes code, reports done, and
// the gates find out afterwards. The commands it may run without confirmation
// are the ones the project declared as its gates — read here rather than passed
// in, because a caller that forgot would quietly produce a session that cannot
// check anything.
func TestAWritableRunCarriesTheProjectsGateCommands(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	for _, gate := range []store.GateConfig{
		{Type: "build_passed", Command: []string{"go", "build", "./..."}, Timeout: time.Minute,
			Required: true, SuccessValue: "true", Kind: "build"},
		{Type: "tests_passed", Command: []string{"go", "test", "-count=1", "./..."}, Timeout: time.Minute,
			Required: true, SuccessValue: "true", Kind: "test"},
	} {
		if err := s.UpsertGate(ctx, project.ID, gate); err != nil {
			t.Fatal(err)
		}
	}
	fake := completedProvider()
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Run(ctx, Request{RunID: "RUN-1", Prompt: "p", Project: project,
		WorkspaceWrite: true}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fake.lastRequest.AllowedCommands, "|")
	for _, want := range []string{"go build ./...", "go test -count=1 ./...", "go build", "go test"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
}

// A read-only run allows nothing. Planning is looking rather than acting, and
// the allowlist would make the distinction a label.
func TestAReadOnlyRunCarriesNoAllowedCommands(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	if err := s.UpsertGate(ctx, project.ID, store.GateConfig{Type: "tests_passed",
		Command: []string{"go", "test", "./..."}, Timeout: time.Minute, Required: true,
		SuccessValue: "true", Kind: "test"}); err != nil {
		t.Fatal(err)
	}
	fake := completedProvider()
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Run(ctx, Request{RunID: "RUN-1", Prompt: "p", Project: project}); err != nil {
		t.Fatal(err)
	}
	if len(fake.lastRequest.AllowedCommands) != 0 {
		t.Fatalf("allowed=%v", fake.lastRequest.AllowedCommands)
	}
}

// A project with no gates allows nothing rather than a default list. It has told
// us nothing about what it is safe to run — and it cannot be run at all until it
// has gates, so there is nothing to make convenient.
func TestAProjectWithNoGatesCarriesNoAllowedCommands(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	fake := completedProvider()
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Run(ctx, Request{RunID: "RUN-1", Prompt: "p", Project: project,
		WorkspaceWrite: true}); err != nil {
		t.Fatal(err)
	}
	if len(fake.lastRequest.AllowedCommands) != 0 {
		t.Fatalf("allowed=%v", fake.lastRequest.AllowedCommands)
	}
}

// A run with a work item claims it, including the resumed path that never went
// through the selection claim.
//
// Pinned at this level because the invariant lives in StartRun and reaches it
// only if the orchestrator passes the work item through. Dropping that argument
// would leave the item on the board while a run worked on it: it would not
// count against the WIP limit, the next selection could pick it again, and
// every later status update guarded on IN_PROGRESS would be a no-op — which is
// how a run that verified left its item in BACKLOG.
func TestARunClaimsItsWorkItemWhateverPathStartedIt(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	goal, err := s.SetGoal(ctx, project.ID, "ship", "o", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "W-RESUME", GoalID: goal.ID,
		Type: "IMPLEMENT", Title: "resumed", Priority: 5, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	fake := completedProvider()
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	// No claim beforehand: this is the state a resumed run starts from.
	if _, err = o.Run(ctx, Request{RunID: "RUN-1", Prompt: "p", WorkItemID: "W-RESUME",
		Project: project, WorkspaceWrite: true}); err != nil {
		t.Fatal(err)
	}
	item, err := s.WorkItemByID(ctx, goal.ID, "W-RESUME")
	if err != nil {
		t.Fatal(err)
	}
	// VERIFYING because the run finished its turn; what matters is that it left
	// BACKLOG at all, which only the claim in StartRun can have done.
	if item.Status == "BACKLOG" {
		t.Fatal("a run worked on this item and it is still on the board")
	}
}
