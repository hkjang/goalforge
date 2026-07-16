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
		blocked, count, err := guard.Record(ctx, p.ID, "W1", "same_error", "test-failed", "R")
		if err != nil {
			t.Fatal(err)
		}
		if blocked != (i == 3) || count != i {
			t.Fatalf("i=%d blocked=%v count=%d", i, blocked, count)
		}
	}
	blocked, count, err := guard.Record(ctx, p.ID, "W1", "no_change", "none", "R4")
	if err != nil || blocked || count != 1 {
		t.Fatalf("blocked=%v count=%d err=%v", blocked, count, err)
	}
	blocked, _, _ = guard.Record(ctx, p.ID, "W1", "no_change", "none", "R5")
	if !blocked {
		t.Fatal("second no-change run must block")
	}
	project, err := s.ProjectByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if project.State != "BLOCKED" {
		t.Fatalf("state=%s", project.State)
	}
}
