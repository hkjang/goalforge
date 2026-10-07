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
	seen map[string]*reservation
	now  func() time.Time
}

// Each reservation has its own identity, even when the clock has not moved.
type reservation struct {
	at time.Time
}

var defaultSuppressor = &suppressor{seen: map[string]*reservation{}, now: time.Now}

// allow reserves an event before sending it. Failed sends release their own
// reservation; successful sends retain the original send-start timestamp.
// Repeats of the same project, state, and reason inside the window are
// dropped; a changed reason is always news and goes through.
func (s *suppressor) allow(key string, window time.Duration) (*reservation, bool) {
	if window <= 0 {
		return nil, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if last, ok := s.seen[key]; ok && now.Sub(last.at) < window {
		return nil, false
	}
	// Entries older than the window can never suppress anything again, so
	// they are dropped rather than growing the map for the process lifetime.
	for existing, entry := range s.seen {
		if now.Sub(entry.at) >= window {
			delete(s.seen, existing)
		}
	}
	entry := &reservation{at: now}
	s.seen[key] = entry
	return entry, true
}

// release must not remove a newer reservation when an old send fails late.
func (s *suppressor) release(key string, entry *reservation) {
	if entry == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[key] == entry {
		delete(s.seen, key)
	}
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
