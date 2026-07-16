package gitops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeVerifiedMergesCleanAndAbortsConflicts(t *testing.T) {
	ctx := context.Background()
	repository := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repository}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repository, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "goalforge@example.invalid")
	run("config", "user.name", "GoalForge Test")
	write("README.md", "base")
	run("add", "README.md")
	run("commit", "-m", "base")
	run("checkout", "-b", "goalforge/P1-W1")
	write("feature.go", "package feature")
	run("add", "feature.go")
	run("commit", "-m", "feature")
	run("checkout", "main")

	message := "Merge verified work W1\n\nGoal-ID: G1\nWork-Item-ID: W1\nRun-ID: R1\n"
	sha, err := MergeVerified(ctx, repository, "main", "goalforge/P1-W1", message)
	if err != nil || sha == "" {
		t.Fatalf("sha=%q err=%v", sha, err)
	}
	log := run("log", "-1", "--format=%an%n%B")
	for _, expected := range []string{"GoalForge", "Merge verified work W1", "Work-Item-ID: W1"} {
		if !strings.Contains(log, expected) {
			t.Fatalf("missing %q in merge commit %q", expected, log)
		}
	}
	if _, statErr := os.Stat(filepath.Join(repository, "feature.go")); statErr != nil {
		t.Fatalf("merged file missing: %v", statErr)
	}

	// A conflicting branch must abort cleanly instead of auto-resolving.
	run("checkout", "-b", "goalforge/P1-W2")
	write("README.md", "branch change")
	run("add", "README.md")
	run("commit", "-m", "branch side")
	run("checkout", "main")
	write("README.md", "main change")
	run("add", "README.md")
	run("commit", "-m", "main side")
	if _, err = MergeVerified(ctx, repository, "main", "goalforge/P1-W2", message); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("expected ErrMergeConflict, got %v", err)
	}
	if status := run("status", "--porcelain"); status != "" {
		t.Fatalf("repository dirty after aborted merge: %q", status)
	}
	// Wrong checked-out branch is refused before touching anything.
	run("checkout", "goalforge/P1-W2")
	if _, err = MergeVerified(ctx, repository, "main", "goalforge/P1-W1", message); err == nil {
		t.Fatal("merge must require the default branch to be checked out")
	}
}

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
