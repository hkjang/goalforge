package policy

import "testing"

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
