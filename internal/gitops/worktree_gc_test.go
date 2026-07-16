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

func TestRemoveWorktreeRefusesDirtyThenRemovesCleanAndForced(t *testing.T) {
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
	worktree, err := EnsureWorktree(ctx, repository, "P1", "WORK-GC")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(worktree.Path, "wip.go"), []byte("package wip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = RemoveWorktree(ctx, repository, worktree, false); !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("expected ErrWorktreeDirty, got %v", err)
	}
	if _, err = os.Stat(worktree.Path); err != nil {
		t.Fatalf("dirty worktree must be preserved: %v", err)
	}
	if err = os.Remove(filepath.Join(worktree.Path, "wip.go")); err != nil {
		t.Fatal(err)
	}
	if err = RemoveWorktree(ctx, repository, worktree, false); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(worktree.Path); !os.IsNotExist(err) {
		t.Fatalf("clean worktree not removed: %v", err)
	}
	// The branch must survive so committed work stays reachable.
	branches := run(repository, "branch", "--list", worktree.Branch)
	if !strings.Contains(branches, worktree.Branch) {
		t.Fatalf("branch was deleted: %q", branches)
	}
	// A dirty worktree is discarded when forced.
	forced, err := EnsureWorktree(ctx, repository, "P1", "WORK-GC-2")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(forced.Path, "wip.go"), []byte("package wip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = RemoveWorktree(ctx, repository, forced, true); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(forced.Path); !os.IsNotExist(err) {
		t.Fatalf("forced worktree not removed: %v", err)
	}
	// Removing an already-missing worktree is idempotent.
	if err = RemoveWorktree(ctx, repository, forced, false); err != nil {
		t.Fatal(err)
	}
}
