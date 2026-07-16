package policy

import (
	"testing"

	"github.com/goalforge/goalforge/internal/gitops"
)

func TestOutOfScopeChangesUsesEvidencePaths(t *testing.T) {
	changes := []gitops.FileChange{{Path: "internal/session/store.go"}, {Path: "internal/session/store_test.go"}, {Path: "README.md"}}
	violations := OutOfScopeChanges("internal/session/**, docs/*.md", changes)
	if len(violations) != 1 || violations[0] != "README.md" {
		t.Fatalf("violations=%v", violations)
	}
	if missing := OutOfScopeChanges("", changes[:1]); len(missing) != 1 {
		t.Fatalf("empty scope accepted changes: %v", missing)
	}
}
