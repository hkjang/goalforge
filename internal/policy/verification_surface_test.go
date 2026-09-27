package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/gitops"
)

// Only gate commands that live inside the repository are part of the surface:
// a session cannot reach a system binary, and treating every gate as in-repo
// would make the check noise nobody reads.
func TestVerificationSurfaceCoversOnlyInRepositoryGates(t *testing.T) {
	repo := t.TempDir()
	script := filepath.Join(repo, "scripts", "verify.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	surface := VerificationSurface(repo, []GateCommand{
		{Type: "unit", Command: []string{"go", "test", "./..."}},
		{Type: "custom", Command: []string{script}},
		{Type: "relative", Command: []string{"scripts/verify.sh", "--fast"}},
		{Type: "outside", Command: []string{"/usr/bin/true"}},
	})
	if len(surface) != 1 || surface[0] != "scripts/verify.sh" {
		t.Fatalf("surface=%v", surface)
	}
}

// A gate the run rewrote cannot be the gate that judges it. The baseline is
// what the verdict is taken against.
func TestSurfaceBaselineDetectsAndRestores(t *testing.T) {
	repo := t.TempDir()
	script := filepath.Join(repo, "verify.sh")
	original := []byte("#!/bin/sh\ngrep -q expected result.txt\n")
	if err := os.WriteFile(script, original, 0o700); err != nil {
		t.Fatal(err)
	}
	baseline, err := CaptureSurface(repo, []string{"verify.sh"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := baseline.Changed(repo)
	if err != nil || len(changed) != 0 {
		t.Fatalf("unchanged: %v err=%v", changed, err)
	}
	// The session weakens its own gate.
	if err = os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if changed, err = baseline.Changed(repo); err != nil || len(changed) != 1 {
		t.Fatalf("a rewritten gate must be detected: %v err=%v", changed, err)
	}
	if err = baseline.Restore(repo); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(script)
	if err != nil || string(restored) != string(original) {
		t.Fatalf("restore did not put the agreed gate back: %q err=%v", restored, err)
	}
	// Deleting the gate counts as changing it.
	if err = os.Remove(script); err != nil {
		t.Fatal(err)
	}
	changed, err = baseline.Changed(repo)
	if err != nil || len(changed) != 1 || !strings.Contains(changed[0], "삭제") {
		t.Fatalf("a deleted gate must be detected: %v err=%v", changed, err)
	}
	if err = baseline.Restore(repo); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(script); err != nil {
		t.Fatalf("restore must recreate a deleted gate: %v", err)
	}
}

// Adding tests is ordinary work; removing the ones that were already there is
// the cheapest way to make a failing gate pass.
func TestRemovedTestsCountsOnlyDeletions(t *testing.T) {
	removed := RemovedTests([]gitops.FileChange{
		{Path: "internal/api/server_test.go", ChangeType: "DELETED"},
		{Path: "internal/api/new_test.go", ChangeType: "ADDED"},
		{Path: "internal/api/work_test.go", ChangeType: "MODIFIED"},
		{Path: "internal/api/server.go", ChangeType: "DELETED"},
	})
	if len(removed) != 1 || removed[0] != "internal/api/server_test.go" {
		t.Fatalf("removed=%v", removed)
	}
}
