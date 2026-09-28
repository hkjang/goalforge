package provider

import "strings"

// sessionGonePhrases are the ways a provider CLI says the session it was asked
// to resume no longer exists.
//
// Each is a phrase the tool prints about its own stored sessions, not a
// general error. The distinction matters: a resume that fails because the
// session is gone is recoverable by starting a fresh one, while a resume that
// fails because the model is down is not, and treating the second as the first
// would silently throw away a conversation over a transient outage.
var sessionGonePhrases = []string{
	// qwen
	"no saved session found",
	"without an id to choose from existing sessions",
	// claude
	"no conversation found with session id",
	// codex / opencode
	"session not found",
	"unknown session",
	"no such session",
}

// SessionGone reports whether a provider's own words say the session being
// resumed does not exist on its side.
//
// GoalForge stores a session ID and keeps handing it to --resume. The provider
// can discard that session at any time — expiry, a cleared cache, a different
// machine — and nothing tells GoalForge. Without this the stored binding is
// retried until the project is marked FAILED and a person has to intervene,
// which is the outcome the tool exists to avoid.
func SessionGone(message string) bool {
	if strings.TrimSpace(message) == "" {
		return false
	}
	lowered := strings.ToLower(message)
	for _, phrase := range sessionGonePhrases {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}
