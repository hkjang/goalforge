package provider

import "testing"

// The real message from a worker run: qwen was asked to resume a session
// GoalForge still had marked ACTIVE, and the CLI had no record of it.
func TestRealProviderMessagesAreRecognised(t *testing.T) {
	for name, message := range map[string]string{
		"qwen missing id": "No saved session found with ID 32e113ab-8c5e-43be-a687-f780f6522654",
		"qwen no id":      "Use `--resume` without an ID to choose from existing sessions.",
		"claude":          "No conversation found with session ID abc-123",
		"codex":           "error: session not found",
		"opencode":        "unknown session: s_01",
	} {
		if !SessionGone(message) {
			t.Errorf("%s: %q must be recognised as a dead session", name, message)
		}
	}
}

// A resume that failed for any other reason must not be treated as a dead
// session: starting fresh would throw away the conversation over a transient
// outage, and the real fault would be hidden behind a retry that looks fine.
func TestOtherFailuresAreNotMistakenForADeadSession(t *testing.T) {
	for name, message := range map[string]string{
		"quota":       "quota exceeded, retry after 3600s",
		"auth":        "authentication failed: invalid API key",
		"model down":  "upstream error: 503 service unavailable",
		"tool error":  "tool execution failed: permission denied",
		"empty":       "",
		"just 'exit'": "qwen exited: exit status 1",
	} {
		if SessionGone(message) {
			t.Errorf("%s: %q must not be treated as a dead session", name, message)
		}
	}
}

func TestMatchingIgnoresCase(t *testing.T) {
	if !SessionGone("NO SAVED SESSION FOUND WITH ID X") {
		t.Error("providers do not agree on casing")
	}
}
