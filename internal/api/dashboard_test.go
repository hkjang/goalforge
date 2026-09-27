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
	script := dashboardHTML
	defined := map[string]bool{}
	for _, match := range regexp.MustCompile(`function ([a-zA-Z0-9_]+)\(`).FindAllStringSubmatch(script, -1) {
		defined[match[1]] = true
	}
	if len(defined) < 20 {
		t.Fatalf("expected the dashboard script, found %d functions", len(defined))
	}
	referenced := map[string]bool{}
	for _, pattern := range []string{`on(?:click|change|keyup)="([a-zA-Z0-9_]+)\(`, `await ([a-zA-Z0-9_]+)\(`} {
		for _, match := range regexp.MustCompile(pattern).FindAllStringSubmatch(script, -1) {
			referenced[match[1]] = true
		}
	}
	builtin := map[string]bool{"api": true, "fetch": true, "prompt": true, "confirm": true, "alert": true, "parseFloat": true, "parseInt": true, "route": true, "if": true}
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
