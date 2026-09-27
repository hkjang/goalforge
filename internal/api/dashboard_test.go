package api

import (
	"regexp"
	"strings"
	"testing"
)

// The dashboard is one embedded script, so a typo in a handler name is only
// discovered by clicking it. This checks that every function referenced from an
// inline handler or from the router is actually defined.
func TestDashboardHandlersAreDefined(t *testing.T) {
	// Only the script: CSS also uses name( syntax for var(), minmax(), and
	// friends, which are not functions this can check.
	start := strings.Index(dashboardHTML, "<script>")
	end := strings.Index(dashboardHTML, "</script>")
	if start < 0 || end < start {
		t.Fatal("dashboard has no script block")
	}
	script := dashboardHTML[start+len("<script>") : end]
	defined := map[string]bool{}
	for _, pattern := range []string{`function ([a-zA-Z0-9_]+)\(`, `(?:var|let|const)\s+([a-zA-Z0-9_]+)\s*=\s*(?:function|\()`} {
		for _, match := range regexp.MustCompile(pattern).FindAllStringSubmatch(script, -1) {
			defined[match[1]] = true
		}
	}
	if len(defined) < 20 {
		t.Fatalf("expected the dashboard script, found %d functions", len(defined))
	}
	// Every call of a bare identifier has to resolve to something. A helper
	// deleted by an edit elsewhere in the file is otherwise only discovered by
	// loading the page and watching it fail.
	referenced := map[string]bool{}
	for _, match := range regexp.MustCompile(`(^|[^.\w"'$])([a-zA-Z_][a-zA-Z0-9_]*)\(`).FindAllStringSubmatch(script, -1) {
		referenced[match[2]] = true
	}
	builtin := map[string]bool{}
	for _, name := range []string{
		// JavaScript keywords that precede a parenthesis.
		"if", "for", "while", "switch", "catch", "return", "function", "typeof", "new", "await", "else", "do",
		// CSS functions appear inside inline-style string literals.
		"var", "calc", "rgba",
		// Globals and constructors the dashboard uses.
		"fetch", "prompt", "confirm", "alert", "parseFloat", "parseInt", "isNaN", "String", "Number", "Date",
		"Array", "Object", "JSON", "Math", "encodeURIComponent", "decodeURIComponent", "setInterval",
		"clearInterval", "setTimeout", "clearTimeout", "TextDecoder", "Error", "Promise",
	} {
		builtin[name] = true
	}
	for name := range referenced {
		if !defined[name] && !builtin[name] {
			t.Errorf("dashboard references %s() but never defines it", name)
		}
	}
	// Views the router can reach must exist.
	for _, view := range []string{"renderList", "renderDetail", "renderWork", "renderApproval", "renderRun"} {
		if !defined[view] {
			t.Errorf("missing view %s", view)
		}
	}
	// The progress rule must stay separate from the resource-burn rule.
	if !strings.Contains(script, "function progressClass(") || !strings.Contains(script, "function gaugeClass(") {
		t.Error("progress and budget colour rules must both be present and distinct")
	}
	// Balanced braces catch an unterminated block that would break the whole
	// script silently in the browser.
	if depth := strings.Count(script, "{") - strings.Count(script, "}"); depth != 0 {
		t.Errorf("unbalanced braces in dashboard script: %d", depth)
	}
}
