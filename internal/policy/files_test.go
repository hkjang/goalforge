package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedFileSnapshotsDetectCreateModifyAndDelete(t *testing.T) {
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, ".env"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := CaptureProtected(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repository, ".env"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repository, "client.key"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := CaptureProtected(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	changed := ChangedProtected(before, after)
	if len(changed) != 2 || changed[0] != ".env" || changed[1] != "client.key" {
		t.Fatalf("changed=%v", changed)
	}
}

func TestProtectedBaselineRestoresOriginalsAndRemovesCreatedSecrets(t *testing.T) {
	ctx := context.Background()
	repository := t.TempDir()
	envPath := filepath.Join(repository, ".env")
	if err := os.WriteFile(envPath, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	baseline, err := CaptureProtectedBaseline(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(envPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(repository, "credentials.json")
	if err = os.WriteFile(created, []byte("created"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = baseline.Restore(ctx, repository); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(envPath)
	if err != nil || string(content) != "original" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if _, err = os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("created protected file still exists: %v", err)
	}
}
