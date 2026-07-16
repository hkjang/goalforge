package planner

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func TestLoopGuardThresholdsPersist(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/loop", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	guard, _ := NewLoopGuard(s, DefaultLoopPolicy())
	for i := 1; i <= 3; i++ {
		action, count, err := guard.Record(ctx, p.ID, "W1", "same_error", "test-failed", "R")
		if err != nil {
			t.Fatal(err)
		}
		expected := LoopNone
		if i == 3 {
			expected = LoopBlock
		}
		if action != expected || count != i {
			t.Fatalf("i=%d action=%s count=%d", i, action, count)
		}
	}
	project, err := s.ProjectByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if project.State != "BLOCKED" {
		t.Fatalf("state=%s", project.State)
	}
}

func TestLoopGuardRotatesSessionBeforeBlockingNoChange(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P2", Name: "demo", RepositoryPath: "/loop2", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	guard, _ := NewLoopGuard(s, DefaultLoopPolicy())
	// NoChange threshold 2: rotate at 2 and 3, block only at 4 (2x threshold).
	expected := []LoopAction{LoopNone, LoopRotateSession, LoopRotateSession, LoopBlock}
	for i, want := range expected {
		action, count, err := guard.Record(ctx, p.ID, "W1", "no_change", "none", "R")
		if err != nil {
			t.Fatal(err)
		}
		if action != want || count != i+1 {
			t.Fatalf("occurrence %d: action=%s count=%d", i+1, action, count)
		}
		project, projectErr := s.ProjectByID(ctx, p.ID)
		if projectErr != nil {
			t.Fatal(projectErr)
		}
		if blocked := project.State == "BLOCKED"; blocked != (want == LoopBlock) {
			t.Fatalf("occurrence %d: state=%s", i+1, project.State)
		}
	}
}
