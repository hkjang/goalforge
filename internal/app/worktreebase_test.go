package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/orchestrator"
	"github.com/goalforge/goalforge/internal/planner"
	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/testscript"
	"github.com/goalforge/goalforge/internal/verification"
)

// The headline behaviour. Each work item runs in its own worktree, and every
// worktree used to be cut from the default branch — so the second item could
// not see what the first had built. In a real run that was:
//
//	main.go:11:2: no required module provides package example.com/notes/store
//	stat …/IDEA-…/store: directory not found
//
// Verified work is never merged without an approval, so the default branch
// stays empty and the next item starts from nothing. It now starts from the
// last verified commit instead, and the work accumulates.
func TestTheSecondItemSeesWhatTheFirstBuilt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: root, DefaultBranch: "main",
		// auto-commit off, which is the default. Verified work in a worktree is
		// committed on its own branch regardless: that branch is where the work
		// is preserved, and CommitVerified already refuses the default branch,
		// so the flag was never what made this safe.
		Provider: "fake", WorktreeEnabled: true}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "o", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"W1", "W2"} {
		if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: id, GoalID: goal.ID, Type: "IMPLEMENT",
			Title: id, Priority: map[string]float64{"W1": 20, "W2": 10}[id], Weight: 1,
			ChangeScope: "store/**,main.go"}); err != nil {
			t.Fatal(err)
		}
	}
	script := testscript.Write(t, root, "verify", "exit 0", "exit /b 0")
	for _, args := range [][]string{{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@e.com"}, {"config", "user.name", "T"},
		{"add", filepath.Base(script)}, {"commit", "-q", "-m", "fixture"}} {
		if out, gitErr := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); gitErr != nil {
			t.Skipf("git unavailable: %v %s", gitErr, out)
		}
	}
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed",
		Command: []string{script}, Timeout: time.Minute, Required: true,
		SuccessValue: "true", Kind: "build"}); err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	// The first turn writes store/store.go; the second asserts it can see it,
	// which is the whole question.
	var secondSaw error
	turn := 0
	fake := &fakeProvider{onStart: func(request provider.RunRequest) {
		turn++
		if turn == 1 {
			if mkErr := os.MkdirAll(filepath.Join(request.WorkDir, "store"), 0o755); mkErr != nil {
				t.Fatalf("provider: %v", mkErr)
			}
			_ = os.WriteFile(filepath.Join(request.WorkDir, "store", "store.go"),
				[]byte("package store\n"), 0o600)
			return
		}
		if _, statErr := os.Stat(filepath.Join(request.WorkDir, "store", "store.go")); statErr != nil {
			secondSaw = fmt.Errorf("the second item cannot see the first item's work: %w", statErr)
		}
		_ = os.WriteFile(filepath.Join(request.WorkDir, "main.go"), []byte("package main\n"), 0o600)
	}}
	runner, _ := orchestrator.New(db, fake)
	verify, _ := verification.New(db, 1024)
	run := 0
	service, _ := New(db, plannerService, runner, verify, func() string {
		run++
		return fmt.Sprintf("RUN-%d", run)
	})
	for i := 0; i < 2; i++ {
		result, continueErr := service.Continue(ctx, project)
		if continueErr != nil {
			t.Fatalf("turn %d: %v", i+1, continueErr)
		}
		if !result.Verification.Passed {
			t.Fatalf("turn %d did not pass: %+v", i+1, result.Verification)
		}
		if err = db.TransitionProjectState(ctx, project.ID, "CHECKPOINTING", "READY"); err != nil {
			// Already READY after a completed checkpoint; not an error here.
			_ = err
		}
	}
	if secondSaw != nil {
		t.Fatal(secondSaw)
	}
	// And nothing reached the default branch: the approval boundary did not
	// move to make this work.
	if _, err = os.Stat(filepath.Join(root, "store")); !os.IsNotExist(err) {
		t.Fatalf("verified work must still need an approval to be merged: %v", err)
	}
}

// Verified work in an isolated worktree is committed on its own branch even
// with auto-commit off, which is the default.
//
// Without it the work is dirty files in a worktree: nothing can merge it,
// nothing can inherit it, and cleaning the worktree loses it. The flag was not
// what kept this safe — CommitVerified refuses the protected branch itself, so
// a non-isolated run on main is still refused.
func TestVerifiedWorkInAWorktreeIsCommittedWithAutoCommitOff(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: root, DefaultBranch: "main",
		Provider: "fake", WorktreeEnabled: true}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "o", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "t", Priority: 10, Weight: 1, ChangeScope: "store/**"}); err != nil {
		t.Fatal(err)
	}
	script := testscript.Write(t, root, "verify", "exit 0", "exit /b 0")
	for _, args := range [][]string{{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@e.com"}, {"config", "user.name", "T"},
		{"add", filepath.Base(script)}, {"commit", "-q", "-m", "fixture"}} {
		if out, gitErr := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); gitErr != nil {
			t.Skipf("git unavailable: %v %s", gitErr, out)
		}
	}
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed",
		Command: []string{script}, Timeout: time.Minute, Required: true,
		SuccessValue: "true", Kind: "build"}); err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	fake := &fakeProvider{onStart: func(request provider.RunRequest) {
		_ = os.MkdirAll(filepath.Join(request.WorkDir, "store"), 0o755)
		_ = os.WriteFile(filepath.Join(request.WorkDir, "store", "store.go"),
			[]byte("package store\n"), 0o600)
	}}
	runner, _ := orchestrator.New(db, fake)
	verify, _ := verification.New(db, 1024)
	service, _ := New(db, plannerService, runner, verify, func() string { return "RUN-1" })
	if _, err = service.Continue(ctx, project); err != nil {
		t.Fatal(err)
	}
	commit, err := db.LatestRunCommitForWork(ctx, project.ID, "W1")
	if err != nil {
		t.Fatalf("verified work must be committed so it can be merged: %v", err)
	}
	if commit.CommitSHA == "" || commit.Branch == "main" {
		t.Fatalf("on its own branch, not the default one: %+v", commit)
	}
	// And the default branch is untouched: committing on a branch is not
	// publishing, and nothing reaches main without an approval.
	if _, err = os.Stat(filepath.Join(root, "store")); !os.IsNotExist(err) {
		t.Fatalf("nothing may reach the default branch here: %v", err)
	}
}
