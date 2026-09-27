package policy

import (
	"testing"

	"github.com/goalforge/goalforge/internal/gitops"
)

func TestScopesOverlap(t *testing.T) {
	for _, tc := range []struct {
		name, left, right string
		overlap           bool
	}{
		{"disjoint packages", "internal/api/**", "internal/store/**", false},
		{"same package", "internal/api/**", "internal/api/**", true},
		{"parent and child", "internal/**", "internal/api/**", true},
		{"empty means anywhere", "", "internal/api/**", true},
		{"both empty", "", "", true},
		{"distinct files", "cmd/main.go", "internal/api/server.go", false},
		{"same file", "cmd/main.go", "cmd/main.go", true},
		{"wildcard inside a shared directory", "internal/api/*.go", "internal/api/server.go", true},
		{"multiple patterns, one overlapping", "docs/**,internal/api/**", "internal/api/**", true},
		{"multiple patterns, none overlapping", "docs/**,README.md", "internal/api/**", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScopesOverlap(tc.left, tc.right); got != tc.overlap {
				t.Fatalf("ScopesOverlap(%q,%q)=%t want %t", tc.left, tc.right, got, tc.overlap)
			}
			if got := ScopesOverlap(tc.right, tc.left); got != tc.overlap {
				t.Fatalf("not symmetric for %q/%q", tc.left, tc.right)
			}
		})
	}
}

func TestDeletedTestFiles(t *testing.T) {
	changes := []gitops.FileChange{
		{Path: "internal/api/server_test.go", ChangeType: "deleted"},
		{Path: "tests/e2e/login.spec.ts", ChangeType: "deleted"},
		{Path: "internal/api/server.go", ChangeType: "deleted"},
		{Path: "internal/api/work_test.go", ChangeType: "modified"},
	}
	deleted := DeletedTestFiles(changes)
	if len(deleted) != 2 {
		t.Fatalf("only deleted test files count: %v", deleted)
	}
	for _, path := range []string{"internal/api/server_test.go", "tests/e2e/login.spec.ts"} {
		found := false
		for _, got := range deleted {
			if got == path {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s in %v", path, deleted)
		}
	}
}

func TestIsTestPath(t *testing.T) {
	for path, expected := range map[string]bool{
		"internal/api/server_test.go":  true,
		"tests/login.spec.ts":          true,
		"src/__tests__/button.test.js": true,
		"test_login.py":                true,
		"src/UserTest.java":            true,
		"internal/api/server.go":       false,
		"docs/testing.md":              false,
	} {
		if got := IsTestPath(path); got != expected {
			t.Errorf("IsTestPath(%q)=%t want %t", path, got, expected)
		}
	}
}
