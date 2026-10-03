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

func TestPathInScopeDoubleStar(t *testing.T) {
	cases := []struct {
		scope, file string
		want        bool
	}{
		{"**", "internal/md/md.go", true},
		{"**", "README.md", true},
		{"internal/**", "internal/md/md.go", true},
		{"internal/**", "cmd/x.go", false},
		{"internal/**/*_test.go", "internal/md/md_test.go", true},
		{"internal/**/*_test.go", "internal/md/md.go", false},
		{"**/*.go", "cmd/mdsite/main.go", true},
		{"**/*.go", "main.go", true},
		{"**/*.go", "README.md", false},
		{"cmd/**,go.mod", "go.mod", true},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},
	}
	for _, c := range cases {
		if got := PathInScope(c.scope, c.file); got != c.want {
			t.Errorf("PathInScope(%q, %q) = %v, want %v", c.scope, c.file, got, c.want)
		}
	}
}
