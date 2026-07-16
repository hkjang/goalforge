package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushBranchPublishesToLocalRemote(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	bare := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run(bare, "init", "--bare", "-b", "main")
	run(source, "init", "-b", "main")
	run(source, "config", "user.email", "goalforge@example.invalid")
	run(source, "config", "user.name", "GoalForge Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(source, "add", "README.md")
	run(source, "commit", "-m", "base")
	run(source, "remote", "add", "origin", bare)
	run(source, "checkout", "-b", "goalforge/P1-WORK-1")
	if err := os.WriteFile(filepath.Join(source, "feature.go"), []byte("package feature"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(source, "add", "feature.go")
	run(source, "commit", "-m", "feature")
	if err := PushBranch(ctx, source, "origin", "goalforge/P1-WORK-1"); err != nil {
		t.Fatal(err)
	}
	local := run(source, "rev-parse", "goalforge/P1-WORK-1")
	remote := run(bare, "rev-parse", "goalforge/P1-WORK-1")
	if local != remote {
		t.Fatalf("remote branch mismatch: local=%s remote=%s", local, remote)
	}
	if err := PushBranch(ctx, source, "origin", ""); err == nil {
		t.Fatal("empty branch must be rejected")
	}
}

func TestCommitVerifiedCreatesTrailedCommitOffProtectedBranch(t *testing.T) {
	ctx := context.Background()
	repository := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run(repository, "init", "-b", "main")
	run(repository, "config", "user.email", "goalforge@example.invalid")
	run(repository, "config", "user.name", "GoalForge Test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repository, "add", "README.md")
	run(repository, "commit", "-m", "base")

	// GIT-003: the protected default branch must be refused.
	if err := os.WriteFile(filepath.Join(repository, "generated.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitVerified(ctx, repository, "main", "GOAL-1", "WORK-1", "RUN-1", "feature"); err == nil {
		t.Fatal("expected protected branch refusal")
	}

	worktree, err := EnsureWorktree(ctx, repository, "P1", "WORK-1")
	if err != nil {
		t.Fatal(err)
	}
	// A clean tree is a no-op, not an error.
	clean, err := CommitVerified(ctx, worktree.Path, "main", "GOAL-1", "WORK-1", "RUN-1", "feature")
	if err != nil || clean.CommitSHA != "" {
		t.Fatalf("clean=%+v err=%v", clean, err)
	}
	if err = os.WriteFile(filepath.Join(worktree.Path, "generated.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit, err := CommitVerified(ctx, worktree.Path, "main", "GOAL-1", "WORK-1", "RUN-1", "implement feature")
	if err != nil {
		t.Fatal(err)
	}
	if commit.CommitSHA == "" || commit.Branch != worktree.Branch || commit.FilesCommitted != 1 {
		t.Fatalf("commit=%+v", commit)
	}
	message := run(worktree.Path, "log", "-1", "--format=%B")
	for _, expected := range []string{"implement feature", "Goal-ID: GOAL-1", "Work-Item-ID: WORK-1", "Run-ID: RUN-1"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("missing %q in commit message %q", expected, message)
		}
	}
	author := run(worktree.Path, "log", "-1", "--format=%an <%ae>")
	if author != "GoalForge <goalforge@goalforge.invalid>" {
		t.Fatalf("author=%q", author)
	}
	status := run(worktree.Path, "status", "--porcelain")
	if status != "" {
		t.Fatalf("tree not clean after commit: %q", status)
	}
}
