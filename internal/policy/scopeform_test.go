package policy

import "testing"

// A work item's change scope is compared against file paths as a list of glob
// patterns. Anything that is not such a list matches nothing, and a work item
// whose scope matches nothing is one that can never run: every file the
// session writes is reported as out of scope and the run is refused.
//
// Nothing checked the form. `ideas` asked a model for an "expected change
// scope" with no format stated, got a prose paragraph, and filed it — so every
// item it generated failed its implementation run with "changed files outside
// declared scope". The chain from a topic to working code stopped there.
func TestAProseScopeIsNotAUsablePattern(t *testing.T) {
	prose := "handler.go 신설: JSON 본문 파싱, http/https 스킴 검증, 잘못된 입력은 400"
	if UsableScope(prose) {
		t.Fatal("a sentence is not a list of path patterns")
	}
	// The thing the sentence is about would have been fine.
	for _, scope := range []string{"handler.go", "handler.go,store.go", "internal/server/**",
		"web/src/**,web/package.json", "*.go"} {
		if !UsableScope(scope) {
			t.Fatalf("%q is a usable scope", scope)
		}
	}
}

// An empty scope is refused separately and for a different reason, so it is
// not this function's answer to give.
func TestAnEmptyScopeIsNotUsable(t *testing.T) {
	for _, scope := range []string{"", "   ", ",", " , "} {
		if UsableScope(scope) {
			t.Fatalf("%q declares nothing", scope)
		}
	}
}

// What makes a pattern unusable is that it cannot be a path: whitespace inside
// a segment, or the punctuation of prose. A long path is still a path.
func TestWhatMakesAPatternUnusable(t *testing.T) {
	unusable := map[string]string{
		"handler.go 신설":        "a space makes two things, and the second is not a path",
		"store.go 를 바꾼다":       "a sentence about a file is not the file",
		"main.go: hello 를 교체":  "a colon introduces prose",
		"internal/**, 그리고 테스트": "a list item that is not a path",
		"a/b.go\nc/d.go":       "a newline is not a separator here",
		// Punctuation with no whitespace, so the whitespace rule cannot be
		// what refuses these. A wrong separator is the likely mistake: the
		// scope looks like paths and matches none of them.
		"store.go;handler.go": "a semicolon is not the separator; a comma is",
		"main.go:hello":       "a colon is not part of a path",
		"handler.go(신설)":      "parentheses annotate; they are not a path",
		`"handler.go"`:        "a quoted path is not the path",
	}
	for scope, why := range unusable {
		if UsableScope(scope) {
			t.Fatalf("%q: %s", scope, why)
		}
	}
	usable := []string{"internal/store/sqlite/store.go", "cmd/goalforge/**",
		"web/src/components/Note.tsx", "docs/*.md", "a_b-c.go"}
	for _, scope := range usable {
		if !UsableScope(scope) {
			t.Fatalf("%q is an ordinary path pattern", scope)
		}
	}
}

// A scope that is usable in form still has to match what gets written; this
// says nothing about that. The two failures need different remedies — one is
// "say it as paths", the other is "you changed the wrong files".
func TestUsableFormIsNotTheSameAsMatching(t *testing.T) {
	if !UsableScope("handler.go") {
		t.Fatal("well formed")
	}
	if PathInScope("handler.go", "store.go") {
		t.Fatal("and it does not match a different file")
	}
}
