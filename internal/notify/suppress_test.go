package notify

import (
	"testing"
	"time"
)

// A blocked project re-reports its block on every worker tick. Without a
// window the webhook becomes noise people learn to ignore, which is worse than
// no webhook at all.
func TestSuppressorDropsRepeatsWithinTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	s := &suppressor{seen: map[string]*reservation{}, now: func() time.Time { return now }}
	window := 30 * time.Minute
	if _, allowed := s.allow("p|BLOCKED|loop", window); !allowed {
		t.Fatal("the first notification must go through")
	}
	if _, allowed := s.allow("p|BLOCKED|loop", window); allowed {
		t.Fatal("an identical repeat must be suppressed")
	}
	// A different reason is news even for the same project and state.
	if _, allowed := s.allow("p|BLOCKED|quota", window); !allowed {
		t.Fatal("a changed reason must go through")
	}
	now = now.Add(window + time.Second)
	if _, allowed := s.allow("p|BLOCKED|loop", window); !allowed {
		t.Fatal("the same event must be reportable again after the window")
	}
	// Expired entries are dropped rather than growing for the process lifetime.
	if len(s.seen) != 1 {
		t.Fatalf("expired keys should be pruned, have %d", len(s.seen))
	}
}

// A zero window disables suppression entirely, which is what an operator
// setting the window to 0 is asking for.
func TestSuppressorWindowZeroAllowsEverything(t *testing.T) {
	s := &suppressor{seen: map[string]*reservation{}, now: time.Now}
	for i := 0; i < 3; i++ {
		if _, allowed := s.allow("p|BLOCKED|loop", 0); !allowed {
			t.Fatal("a zero window must not suppress")
		}
	}
}

func TestSuppressorOldReleasePreservesNewReservation(t *testing.T) {
	for _, replacement := range []string{"expired", "released at same time"} {
		t.Run(replacement, func(t *testing.T) {
			now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
			s := &suppressor{seen: map[string]*reservation{}, now: func() time.Time { return now }}
			const key = "p|BLOCKED|loop"
			const window = 30 * time.Minute
			old, allowed := s.allow(key, window)
			if !allowed {
				t.Fatal("first reservation must be allowed")
			}
			if replacement == "expired" {
				now = now.Add(window)
			} else {
				s.release(key, old)
			}
			current, allowed := s.allow(key, window)
			if !allowed {
				t.Fatal("replacement reservation must be allowed")
			}
			s.release(key, old)
			if _, allowed := s.allow(key, window); allowed {
				t.Fatal("an old release removed the current reservation")
			}
			s.release(key, current)
			if _, allowed := s.allow(key, window); !allowed {
				t.Fatal("releasing the current reservation must allow another attempt")
			}
		})
	}
}

func TestEventLabelPrefersTheName(t *testing.T) {
	if got := (Event{Project: "PRJ-1", Name: "checkout"}).label(); got != "checkout" {
		t.Fatalf("label=%q", got)
	}
	if got := (Event{Project: "PRJ-1"}).label(); got != "PRJ-1" {
		t.Fatalf("label=%q", got)
	}
}
