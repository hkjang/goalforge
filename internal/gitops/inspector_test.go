package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParsePorcelainAndCompare(t *testing.T) {
	files, err := parsePorcelainZ([]byte(" M a.go\x00?? new.txt\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != "a.go" || files[1] != "new.txt" {
		t.Fatalf("files=%v", files)
	}
	saved := Snapshot{CommitSHA: "abc", Branch: "main", DirtyFiles: files}
	if err := EqualSnapshot(saved, Snapshot{CommitSHA: "abc", Branch: "main", DirtyFiles: []string{"new.txt", "a.go"}}); err != nil {
		t.Fatal(err)
	}
	if err := EqualSnapshot(saved, Snapshot{CommitSHA: "def", Branch: "main", DirtyFiles: files}); err == nil {
		t.Fatal("expected commit conflict")
	}
}

func TestSnapshotDetectsDirtyContentChange(t *testing.T) {
	repository := t.TempDir()
	if err := exec.Command("git", "-C", repository, "init", "-b", "main").Run(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repository, "work.go")
	if err := os.WriteFile(path, []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspector := GitInspector{}
	saved, err := inspector.Snapshot(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := inspector.Snapshot(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if saved.DirtyFingerprint == current.DirtyFingerprint {
		t.Fatal("dirty fingerprint did not change")
	}
	if err = EqualSnapshot(saved, current); err == nil {
		t.Fatal("expected dirty content conflict")
	}
}

func TestSnapshotSupportsRepositoryWithoutCommits(t *testing.T) {
	repository := t.TempDir()
	if err := exec.Command("git", "-C", repository, "init", "-b", "main").Run(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (GitInspector{}).Snapshot(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CommitSHA != "" || snapshot.Branch != "main" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
