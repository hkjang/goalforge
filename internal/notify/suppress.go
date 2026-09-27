package notify

import (
	"os"
	"sync"
	"time"
)

// EnvRepeatWindow overrides how long an identical notification is suppressed.
const EnvRepeatWindow = "GOALFORGE_WEBHOOK_REPEAT_WINDOW"

// defaultRepeatWindow is how long the same project/state/reason stays quiet.
// A blocked project re-reports its block on every worker tick; without a
// window the webhook becomes noise people learn to ignore, which is worse
// than no webhook at all.
const defaultRepeatWindow = 30 * time.Minute

type suppressor struct {
	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time
}

var defaultSuppressor = &suppressor{seen: map[string]time.Time{}, now: time.Now}

// allow reports whether an event should be sent, recording it when it is.
// Repeats of the same project, state, and reason inside the window are
// dropped; a changed reason is always news and goes through.
func (s *suppressor) allow(key string, window time.Duration) bool {
	if window <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if last, ok := s.seen[key]; ok && now.Sub(last) < window {
		return false
	}
	// Entries older than the window can never suppress anything again, so
	// they are dropped rather than growing the map for the process lifetime.
	for existing, at := range s.seen {
		if now.Sub(at) >= window {
			delete(s.seen, existing)
		}
	}
	s.seen[key] = now
	return true
}

func repeatWindow() time.Duration {
	if value := os.Getenv(EnvRepeatWindow); value != "" {
		parsed, err := time.ParseDuration(value)
		if err == nil && parsed >= 0 {
			return parsed
		}
	}
	return defaultRepeatWindow
}
