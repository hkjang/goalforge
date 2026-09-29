package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tree is read at a commit, so two readers of the same commit see the same
// repository however dirty either working tree is.
func TestListTreeReadsTheCommitNotTheWorkingTree(t *testing.T) {
	dir := diffRepo(t)
	first := commit(t, dir, "a.txt", "a\n")
	commit(t, dir, "b.txt", "b\n")
	atFirst, err := ListTree(context.Background(), dir, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(atFirst) != 1 || atFirst[0] != "a.txt" {
		t.Fatalf("the first commit has one file: %v", atFirst)
	}
	// A file written but never committed is in no tree, so an assessment can
	// never pass on a change that exists only on one machine.
	if err := os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte("u\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	atFirstAgain, err := ListTree(context.Background(), dir, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(atFirstAgain) != 1 {
		t.Fatalf("a dirty working tree does not change what a commit contains: %v", atFirstAgain)
	}
}

// An absence is a finding; a failure is not. Collapsing them would make a
// broken repository look like a missing feature.
func TestFileAtDistinguishesAbsenceFromFailure(t *testing.T) {
	dir := diffRepo(t)
	sha := commit(t, dir, "go.mod", "module example.com/x\n")
	body, err := FileAt(context.Background(), dir, sha, "go.mod", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "module example.com/x") {
		t.Fatalf("body=%q", body)
	}
	if _, err = FileAt(context.Background(), dir, sha, "package.json", 0); !errors.Is(err, ErrPathNotInTree) {
		t.Fatalf("a missing file must be reported as an absence: %v", err)
	}
}

// Both a commit and a path are validated before git sees them: a ref name works
// by luck, and an option-shaped string is not an argument at all.
func TestTreeReadsRefuseSomethingThatIsNotACommitOrAPath(t *testing.T) {
	dir := diffRepo(t)
	sha := commit(t, dir, "a.txt", "a\n")
	for _, bad := range []string{"HEAD", "main", "", "--upload-pack=touch /tmp/x"} {
		if _, err := ListTree(context.Background(), dir, bad); err == nil {
			t.Fatalf("%q is not a commit SHA", bad)
		}
		if _, err := FileAt(context.Background(), dir, bad, "a.txt", 0); err == nil {
			t.Fatalf("%q is not a commit SHA", bad)
		}
	}
	if _, err := FileAt(context.Background(), dir, sha, "--output=/tmp/pwned", 0); err == nil {
		t.Fatal("a path that looks like an option must be refused")
	}
}

// A repository can hold a very large fixture, and a detector looking for an
// import statement does not need all of it.
func TestFileAtIsCapped(t *testing.T) {
	dir := diffRepo(t)
	sha := commit(t, dir, "big.txt", strings.Repeat("x", 5000))
	body, err := FileAt(context.Background(), dir, sha, "big.txt", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 100 {
		t.Fatalf("len=%d", len(body))
	}
}
