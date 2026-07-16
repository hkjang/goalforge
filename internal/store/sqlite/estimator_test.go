package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
)

func TestEstimateWorkItemTokensAveragesRecentWorkRuns(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P-EST", Name: "demo", RepositoryPath: "/est", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, p.ID, "ship", "objective", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EstimateWorkItemTokens(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound without history, got %v", err)
	}
	record := func(runID, workItemID, turnID string, input, output int64) {
		t.Helper()
		if err := s.StartRun(ctx, RunRecord{ID: runID, ProjectID: p.ID, WorkItemID: workItemID, Provider: "codex"}); err != nil {
			t.Fatal(err)
		}
		event := provider.Event{RunID: runID, Type: provider.EventCompleted, TurnID: turnID, Raw: json.RawMessage(`{"type":"turn.completed"}`), Usage: &provider.Usage{InputTokens: input, OutputTokens: output}}
		if err := s.RecordProviderEvent(ctx, p.ID, event); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, runID, "VERIFYING", "VERIFYING"); err != nil {
			t.Fatal(err)
		}
		if err := s.TransitionProjectState(ctx, p.ID, "VERIFYING", "READY"); err != nil {
			t.Fatal(err)
		}
	}
	record("R1", "W1", "t1", 1000, 500)
	record("R2", "W1", "t2", 2000, 1000)
	// Runs without a work item (isolated discovery) must not skew the estimate.
	record("R3", "", "t3", 99999, 0)
	estimate, err := s.EstimateWorkItemTokens(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Work runs total 1500 and 3000 tokens: average 2250 plus a 50% margin.
	if estimate != 3375 {
		t.Fatalf("estimate=%d", estimate)
	}
}

func TestTurnsArePersistedWithStickyTerminalStatus(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P-TURN", Name: "demo", RepositoryPath: "/turn", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetGoal(ctx, p.ID, "ship", "objective", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, RunRecord{ID: "R1", ProjectID: p.ID, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	emit := func(eventType, turnID string) {
		t.Helper()
		if err := s.RecordProviderEvent(ctx, p.ID, provider.Event{RunID: "R1", Type: eventType, TurnID: turnID, Raw: json.RawMessage(`{"type":"` + eventType + `","turn":"` + turnID + `"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	emit(provider.EventMessage, "t1")
	emit(provider.EventCompleted, "t1")
	// A late stream event must not downgrade the completed turn.
	emit(provider.EventMessage, "t1")
	emit(provider.EventFailed, "t2")
	turns, err := s.ListTurns(ctx, "R1")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || turns[0].ProviderTurnID != "t1" || turns[0].Status != "COMPLETED" || turns[1].ProviderTurnID != "t2" || turns[1].Status != "FAILED" {
		t.Fatalf("turns=%+v", turns)
	}
}
