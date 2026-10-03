package policy

import (
	"path"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/gitops"
)

func OutOfScopeChanges(scope string, changes []gitops.FileChange) []string {
	var violations []string
	for _, change := range changes {
		if !PathInScope(scope, change.Path) {
			violations = append(violations, normalizePath(change.Path))
		}
	}
	sort.Strings(violations)
	return violations
}

// PathInScope reports whether a file matches a declared change scope.
//
// It answers only the pattern question and deliberately does not decide what
// an empty scope means, because the two callers need opposite answers and both
// are right: a work item that declared no scope may change nothing
// (OutOfScopeChanges), while two work items that declared no scope must be
// assumed to collide (ScopesOverlap). A shared helper that picked one would
// silently give the other the unsafe answer. An empty scope therefore matches
// nothing here, and callers say what they mean.
func PathInScope(scope, filePath string) bool {
	patterns := scopePatterns(scope)
	name := normalizePath(filePath)
	for _, pattern := range patterns {
		if matchesPattern(pattern, name) {
			return true
		}
	}
	return false
}

func matchesPattern(pattern, name string) bool {
	switch {
	case strings.Contains(pattern, "**"):
		return globMatch(strings.Split(pattern, "/"), strings.Split(name, "/"))
	case strings.HasSuffix(pattern, "/**"):
		prefix := strings.TrimSuffix(pattern, "/**")
		return name == prefix || strings.HasPrefix(name, prefix+"/")
	case strings.ContainsAny(pattern, "*?["):
		matched, _ := path.Match(pattern, name)
		return matched
	default:
		return name == pattern || strings.HasPrefix(name, strings.TrimSuffix(pattern, "/")+"/")
	}
}

// globMatch matches path segments, where a "**" segment stands for any number
// of segments (including none). path.Match alone cannot: its "*" stops at "/",
// so a bare "**" or "internal/**/x.go" would match nothing.
func globMatch(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for skip := 0; skip <= len(name); skip++ {
			if globMatch(pattern[1:], name[skip:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	if matched, _ := path.Match(pattern[0], name[0]); !matched {
		return false
	}
	return globMatch(pattern[1:], name[1:])
}

func normalizePath(filePath string) string {
	return strings.TrimPrefix(strings.ReplaceAll(filePath, "\\", "/"), "./")
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

// DeletedTestFiles reports test files a change removed. Deleting a test is the
// cheapest way to make a failing gate pass, so a run that does it is surfaced
// for review rather than counted as progress.
func DeletedTestFiles(changes []gitops.FileChange) []string {
	var deleted []string
	for _, change := range changes {
		if !strings.EqualFold(change.ChangeType, "deleted") && !strings.EqualFold(change.ChangeType, "removed") {
			continue
		}
		if IsTestPath(change.Path) {
			deleted = append(deleted, change.Path)
		}
	}
	return deleted
}

// IsTestPath recognizes the common test-file conventions across the languages
// GoalForge's providers are likely to be pointed at.
func IsTestPath(path string) bool {
	name := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	base := name
	if cut := strings.LastIndex(name, "/"); cut >= 0 {
		base = name[cut+1:]
	}
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.HasSuffix(base, "_test.py"), strings.HasPrefix(base, "test_"):
		return true
	case strings.Contains(base, ".test."), strings.Contains(base, ".spec."):
		return true
	case strings.HasSuffix(base, "test.java"), strings.HasSuffix(base, "tests.cs"):
		return true
	}
	for _, directory := range []string{"test/", "tests/", "spec/", "__tests__/"} {
		if strings.HasPrefix(name, directory) || strings.Contains(name, "/"+directory) {
			return true
		}
	}
	return false
}

// UsableScope reports whether a declared change scope is a list of path
// patterns at all.
//
// The scope is compared against file paths, so anything that is not such a
// list matches nothing — and a work item whose scope matches nothing can never
// run: every file the session writes is reported as out of scope and the run
// is refused. Nothing checked the form, so a generator asked for an "expected
// change scope" with no format stated returned a prose paragraph, and every
// item it filed failed its implementation run.
//
// This asks only about form. Whether a well-formed scope matches what actually
// got written is OutOfScopeChanges' question, and the two failures need
// different remedies: one is "say it as paths", the other is "you changed the
// wrong files".
func UsableScope(scope string) bool {
	patterns := scopePatterns(scope)
	if len(patterns) == 0 {
		return false
	}
	for _, pattern := range patterns {
		if !usablePattern(pattern) {
			return false
		}
	}
	return true
}

// usablePattern reports whether one comma-separated entry could be a path.
//
// Whitespace is the signal that separates a path from a sentence about one:
// "handler.go" is a path and "handler.go 신설" is a description whose first
// word happens to be a path. Accepting the second would let a sentence through
// whenever it began with a filename, which is most of the time.
func usablePattern(pattern string) bool {
	if strings.ContainsAny(pattern, " \t\n\r") {
		return false
	}
	// Punctuation that cannot appear in a path but is ordinary in prose. A
	// colon in particular is how a description introduces itself.
	if strings.ContainsAny(pattern, ":;\"'()") {
		return false
	}
	return true
}
