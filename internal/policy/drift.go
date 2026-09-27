package policy

import (
	"path"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/gitops"
)

func OutOfScopeChanges(scope string, changes []gitops.FileChange) []string {
	patterns := strings.Split(scope, ",")
	allowed := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern = strings.TrimSpace(strings.TrimPrefix(pattern, "./")); pattern != "" {
			allowed = append(allowed, pattern)
		}
	}
	var violations []string
	for _, change := range changes {
		name := strings.TrimPrefix(strings.ReplaceAll(change.Path, "\\", "/"), "./")
		matched := false
		for _, pattern := range allowed {
			if strings.HasSuffix(pattern, "/**") {
				prefix := strings.TrimSuffix(pattern, "/**")
				matched = name == prefix || strings.HasPrefix(name, prefix+"/")
			} else if strings.ContainsAny(pattern, "*?[") {
				matched, _ = path.Match(pattern, name)
			} else {
				matched = name == pattern || strings.HasPrefix(name, strings.TrimSuffix(pattern, "/")+"/")
			}
			if matched {
				break
			}
		}
		if !matched {
			violations = append(violations, name)
		}
	}
	sort.Strings(violations)
	return violations
}

// ScopesOverlap reports whether two declared change scopes could touch the
// same files. Running two work items at once is only safe when their scopes
// are disjoint; an empty scope means "anywhere", which overlaps everything.
func ScopesOverlap(left, right string) bool {
	leftPatterns, rightPatterns := scopePatterns(left), scopePatterns(right)
	if len(leftPatterns) == 0 || len(rightPatterns) == 0 {
		return true
	}
	for _, a := range leftPatterns {
		for _, b := range rightPatterns {
			if patternsOverlap(a, b) {
				return true
			}
		}
	}
	return false
}

func scopePatterns(scope string) []string {
	var result []string
	for _, pattern := range strings.Split(scope, ",") {
		if pattern = strings.TrimSpace(strings.TrimPrefix(pattern, "./")); pattern != "" {
			result = append(result, pattern)
		}
	}
	return result
}

// patternsOverlap compares two scope patterns conservatively: anything it
// cannot prove disjoint is treated as overlapping, because a false "safe"
// answer means two sessions editing the same file.
func patternsOverlap(a, b string) bool {
	prefixA, wildA := scopePrefix(a)
	prefixB, wildB := scopePrefix(b)
	if strings.HasPrefix(prefixA, prefixB) || strings.HasPrefix(prefixB, prefixA) {
		return true
	}
	if !wildA && !wildB {
		return prefixA == prefixB
	}
	return false
}

// scopePrefix reduces a pattern to the literal directory prefix before any
// wildcard, which is what decides whether two scopes can reach each other.
func scopePrefix(pattern string) (string, bool) {
	pattern = strings.TrimSuffix(pattern, "/**")
	index := strings.IndexAny(pattern, "*?[")
	if index < 0 {
		return strings.TrimSuffix(pattern, "/"), false
	}
	prefix := pattern[:index]
	if cut := strings.LastIndex(prefix, "/"); cut >= 0 {
		return prefix[:cut], true
	}
	return "", true
}
