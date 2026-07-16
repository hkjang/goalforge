package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEnsureWorktreeCreatesAndReusesDedicatedBranch(t *testing.T) {
	repository := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repository}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "goalforge@example.invalid")
	run("config", "user.name", "GoalForge Test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "base")
	created, err := EnsureWorktree(context.Background(), repository, "P1", "WORK-1")
	if err != nil {
		t.Fatal(err)
	}
	if created.Branch != "goalforge/P1-WORK-1" || created.Path == repository {
		t.Fatalf("worktree=%+v", created)
	}
	if err = os.WriteFile(filepath.Join(created.Path, "work.go"), []byte("package work"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repository, "work.go")); !os.IsNotExist(err) {
		t.Fatalf("worktree change leaked into base repository: %v", err)
	}
	if err = os.WriteFile(filepath.Join(created.Path, "README.md"), []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	changes := []FileChange{{Path: "README.md", ChangeType: "MODIFIED"}, {Path: "work.go", ChangeType: "ADDED"}}
	if err = RollbackWorktree(context.Background(), created, changes); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(created.Path, "README.md"))
	if err != nil || string(content) != "base" {
		t.Fatalf("tracked file not restored: %q err=%v", content, err)
	}
	if _, err = os.Stat(filepath.Join(created.Path, "work.go")); !os.IsNotExist(err) {
		t.Fatalf("added file was not removed: %v", err)
	}
	reused, err := EnsureWorktree(context.Background(), repository, "P1", "WORK-1")
	if err != nil || reused.Path != created.Path || reused.Branch != created.Branch {
		t.Fatalf("reused=%+v err=%v", reused, err)
	}
}
