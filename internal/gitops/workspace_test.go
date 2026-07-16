package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceSnapshotClassifiesChanges(t *testing.T) {
	root := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("modified.go", "before")
	write("deleted.go", "delete")
	write("same.go", "same")
	write(".git/ignored", "before")
	before, err := CaptureWorkspace(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	write("modified.go", "after")
	if err = os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	write("added.go", "add")
	write(".git/ignored", "after")
	after, err := CaptureWorkspace(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	changes := ChangedFiles(before, after)
	if len(changes) != 3 || changes[0].Path != "added.go" || changes[0].ChangeType != "ADDED" || changes[1].ChangeType != "DELETED" || changes[2].ChangeType != "MODIFIED" {
		t.Fatalf("changes=%+v", changes)
	}
}
