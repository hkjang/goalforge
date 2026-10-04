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

func worktreeRepo(t *testing.T) (string, Worktree) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-q", "-m", "base")
	worktree, err := EnsureWorktree(context.Background(), repo, "PRJ-1", "W1", "")
	if err != nil {
		t.Fatal(err)
	}
	return repo, worktree
}

// A worktree that is still there is still there; everything below would be
// meaningless if this did not hold.
func TestIntactWorktreePasses(t *testing.T) {
	_, worktree := worktreeRepo(t)
	if err := WorktreeIntact(context.Background(), worktree); err != nil {
		t.Fatalf("a present worktree must pass: %v", err)
	}
}

// The failure this names: `git worktree prune`, a manual delete, a fresh
// clone, or — once a PostgreSQL queue is shared — a worker on a different
// machine. Unnamed it surfaces as `fatal: cannot change to '...'`, which reads
// like the repository is broken.
func TestDeletedWorktreeIsNamedRatherThanQuotedFromGit(t *testing.T) {
	repo, worktree := worktreeRepo(t)
	if err := os.RemoveAll(worktree.Path); err != nil {
		t.Fatal(err)
	}
	err := WorktreeIntact(context.Background(), worktree)
	if !errors.Is(err, ErrWorktreeMissing) {
		t.Fatalf("a deleted worktree must be named: %v", err)
	}
	if !strings.Contains(err.Error(), worktree.Path) {
		t.Fatalf("the message must name the path: %v", err)
	}
	// What survived decides what the operator does next, so it is stated.
	explanation := ExplainMissingWorktree(repo, worktree)
	for _, want := range []string{worktree.Branch, "worktree add", "커밋되지 않은"} {
		if !strings.Contains(explanation, want) {
			t.Errorf("the explanation must mention %q: %s", want, explanation)
		}
	}
}

// A pruned worktree leaves the files and removes git's link to them, which
// looks present and is not usable.
func TestPrunedWorktreeIsDetected(t *testing.T) {
	repo, worktree := worktreeRepo(t)
	cmd := exec.Command("git", "-C", repo, "worktree", "remove", "--force", worktree.Path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not prune in this environment: %v\n%s", err, out)
	}
	if err := os.MkdirAll(worktree.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WorktreeIntact(context.Background(), worktree); !errors.Is(err, ErrWorktreeMissing) {
		t.Fatalf("a path git no longer knows about must be detected: %v", err)
	}
}

// A path that exists with different work in it is not the work that was left
// there, and continuing in it would write into somebody else's branch.
func TestWorktreeOnADifferentBranchIsRefused(t *testing.T) {
	_, worktree := worktreeRepo(t)
	cmd := exec.Command("git", "-C", worktree.Path, "checkout", "-q", "-b", "someone-else")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	err := WorktreeIntact(context.Background(), worktree)
	if !errors.Is(err, ErrWorktreeDiverged) {
		t.Fatalf("a different branch must be refused: %v", err)
	}
	if !strings.Contains(err.Error(), "someone-else") {
		t.Fatalf("the message must name what is actually there: %v", err)
	}
}

// A record with no path at all is the same kind of finding, not a panic.
func TestEmptyRecordIsAMissingWorktree(t *testing.T) {
	if err := WorktreeIntact(context.Background(), Worktree{}); !errors.Is(err, ErrWorktreeMissing) {
		t.Fatalf("err=%v", err)
	}
}
