package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func relocateFixture(t *testing.T) (context.Context, *Store, model.Project, string) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	original := t.TempDir()
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: original,
		DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	return ctx, s, project, original
}

// A project is found by its repository path, so a repository that moves
// becomes unreachable while its goal, evidence, and approvals are all still in
// the database. Rewriting the path is the whole recovery.
func TestRelocatingAProjectKeepsItsHistory(t *testing.T) {
	ctx, s, project, original := relocateFixture(t)
	goal, err := s.SetGoal(ctx, project.ID, "G", "o", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	moved := t.TempDir()
	relocated, err := s.RelocateProject(ctx, project.ID, moved)
	if err != nil {
		t.Fatal(err)
	}
	if relocated.RepositoryPath != moved {
		t.Fatalf("path=%q want %q", relocated.RepositoryPath, moved)
	}
	// The same project, not a new one: the goal has to come with it.
	found, err := s.ProjectByPath(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != project.ID {
		t.Fatalf("relocating must rename, not re-register: %s vs %s", found.ID, project.ID)
	}
	current, err := s.CurrentGoal(ctx, project.ID)
	if err != nil || current.ID != goal.ID {
		t.Fatalf("the goal must survive the move: %v %+v", err, current)
	}
	// The old path must stop resolving, or two directories answer for one
	// project and later lookups pick arbitrarily.
	if _, err = s.ProjectByPath(ctx, original); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the old path must stop resolving: %v", err)
	}
}

// Two records pointing at one repository would make every later lookup answer
// arbitrarily, so the collision is refused rather than created.
func TestRelocatingOntoAnotherProjectIsRefused(t *testing.T) {
	ctx, s, project, _ := relocateFixture(t)
	occupied := t.TempDir()
	if err := s.CreateProject(ctx, model.Project{ID: "PRJ-2", Name: "other", RepositoryPath: occupied,
		DefaultBranch: "main", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.RelocateProject(ctx, project.ID, occupied)
	if err == nil {
		t.Fatal("moving onto an occupied path must be refused")
	}
	if !strings.Contains(err.Error(), "other") {
		t.Fatalf("the refusal must name what is already there: %v", err)
	}
}

// Pointing a project at a directory that is not there would replace one dead
// end with another.
func TestRelocatingToAMissingDirectoryIsRefused(t *testing.T) {
	ctx, s, project, _ := relocateFixture(t)
	if _, err := s.RelocateProject(ctx, project.ID, filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("a path that does not exist must be refused")
	}
}

// Moving a project that is not registered is a different mistake and says so.
func TestRelocatingAnUnknownProjectIsNotFound(t *testing.T) {
	ctx, s, _, _ := relocateFixture(t)
	if _, err := s.RelocateProject(ctx, "PRJ-GHOST", t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}
