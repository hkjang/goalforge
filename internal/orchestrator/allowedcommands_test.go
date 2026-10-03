package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
