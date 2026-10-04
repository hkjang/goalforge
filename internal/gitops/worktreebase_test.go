package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func baseRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@e.com"}, {"config", "user.name", "T"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	return dir
}

// The defect, at the level it happened. Every worktree was cut from HEAD, so a
// work item could not see what earlier items had built: item 1 wrote store/,
// verified, and was marked done, and item 3's worktree — cut from a default
// branch nothing had been merged into — failed with
// "stat …/store: directory not found".
//
// Given the previous item's commit as a base, the work accumulates.
func TestAWorktreeStartsFromTheGivenBase(t *testing.T) {
	ctx := context.Background()
	repository := baseRepo(t)
	first, err := EnsureWorktree(ctx, repository, "P1", "W1", "")
	if err != nil {
		t.Fatal(err)
	}
	// What item 1 built, committed on its own branch and never merged.
	if err = os.MkdirAll(filepath.Join(first.Path, "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(first.Path, "store", "store.go"),
		[]byte("package store\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "store"}} {
		if out, cmdErr := exec.Command("git", append([]string{"-C", first.Path}, args...)...).CombinedOutput(); cmdErr != nil {
			t.Fatalf("%v %s", cmdErr, out)
		}
	}
	built, err := gitOutput(ctx, first.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// Item 2 starts from item 1's work rather than from the branch.
	second, err := EnsureWorktree(ctx, repository, "P1", "W2", built)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(second.Path, "store", "store.go")); err != nil {
		t.Fatalf("the next item must see what the previous one built: %v", err)
	}
	if second.BaseCommit != built {
		t.Fatalf("base=%s want %s", second.BaseCommit, built)
	}
	// And the default branch is still untouched: nothing was merged.
	if _, err = os.Stat(filepath.Join(repository, "store")); !os.IsNotExist(err) {
		t.Fatalf("the approval boundary did not move: %v", err)
	}
}

// With no base the worktree starts from HEAD, which is a goal's first item and
// every project that has built nothing yet.
func TestAnEmptyBaseMeansHead(t *testing.T) {
	ctx := context.Background()
	repository := baseRepo(t)
	head, err := gitOutput(ctx, repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := EnsureWorktree(ctx, repository, "P1", "W1", "")
	if err != nil {
		t.Fatal(err)
	}
	if worktree.BaseCommit != head {
		t.Fatalf("base=%s head=%s", worktree.BaseCommit, head)
	}
}

// A base this repository does not have is refused with the commit named. Passed
// straight to `git worktree add` the message names neither the commit nor where
// it came from, and the run fails with something nobody can act on.
func TestABaseTheRepositoryDoesNotHaveIsRefused(t *testing.T) {
	_, err := EnsureWorktree(context.Background(), baseRepo(t), "P1", "W1",
		"0000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("that commit is not here")
	}
	// The refusal has to be this one, not git's. Git's message also carries the
	// sha, so matching on that alone passed with the check removed — and git's
	// wording says neither what the commit was for nor where it came from.
	if !strings.Contains(err.Error(), "기준 커밋") {
		t.Fatalf("the base has to be checked before git is asked: %v", err)
	}
	if !strings.Contains(err.Error(), "0000000") {
		t.Fatalf("the refusal must name it: %v", err)
	}
}
