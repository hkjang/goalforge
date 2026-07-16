package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
)

func TestWorktreeCleanupTargetsOnlyTerminalWorkItems(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P-GC", Name: "demo", RepositoryPath: "/gc", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, p.ID, "ship", "objective", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"W-DONE", "W-OPEN"} {
		if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: id, GoalID: goal.ID, Type: "IMPLEMENT", Title: id}); err != nil {
			t.Fatal(err)
		}
		if err = s.RecordWorktree(ctx, p.ID, id, gitops.Worktree{Path: "/gc-worktrees/" + id, Branch: "goalforge/" + id, BaseCommit: "abc"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.SetWorkItemStatus(ctx, goal.ID, "W-DONE", "DONE"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, goal.ID, "W-OPEN", "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	candidates, err := s.WorktreesForCleanup(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].WorkItemID != "W-DONE" {
		t.Fatalf("candidates=%+v", candidates)
	}
	if err = s.MarkWorktreeRemoved(ctx, p.ID, "W-DONE"); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkWorktreeRemoved(ctx, p.ID, "W-DONE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second removal must be ErrNotFound, got %v", err)
	}
	candidates, err = s.WorktreesForCleanup(ctx, p.ID)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
}
