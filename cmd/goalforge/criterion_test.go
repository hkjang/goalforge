package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func TestCLICriterionContract(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-b", "main")
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("GOALFORGE_DB", dbPath)
	t.Chdir(repo)
	runCLI(t, ctx, "project", "init", "--name", "criteria")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Getwd resolves temporary-directory symlinks just as CLI dispatch does.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.ProjectByPath(ctx, cwd)
	if err != nil {
		t.Fatal(err)
	}

	raw := []string{" SaveNote @ JoUrNeY = true ", "latency@test=<=200ms"}
	want := []model.Criterion{
		{Type: "SaveNote", ExpectedValue: "true", RequiredKind: "journey"},
		{Type: "latency", ExpectedValue: "<=200ms", RequiredKind: "test"},
	}
	runCLI(t, ctx, "goal", "set", "--title", "criterion contract", "--objective", "preserve required proof",
		"--criterion", raw[0], "--criterion", raw[1])
	before, err := db.CurrentGoal(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Criteria, want) {
		t.Fatalf("stored criteria = %+v, want %+v", before.Criteria, want)
	}

	for _, tt := range []struct {
		name string
		raw  string
	}{
		{"empty kind", "x@=true"},
		{"unknown kind", "x@unregistered=true"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Put a valid criterion first so an unknown kind exercises rollback
			// after the store has started writing the replacement goal.
			output, err := runCLIWithError(t, ctx, "goal", "set", "--title", "rejected replacement",
				"--objective", "must not replace the active goal", "--reason", "exercise invalid criterion",
				"--criterion", raw[0], "--criterion", tt.raw)
			if err == nil {
				t.Errorf("goal set must reject %q; output: %s", tt.raw, output)
			}
			after, err := db.CurrentGoal(ctx, project.ID)
			if err != nil {
				t.Fatal(err)
			}
			// This includes the active goal's ID, version and full criteria.
			if !reflect.DeepEqual(after, before) {
				t.Errorf("rejected change altered the active goal:\nbefore: %+v\nafter: %+v", before, after)
			}
		})
	}
}
