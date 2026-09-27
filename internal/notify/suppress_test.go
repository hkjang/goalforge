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
	s := &suppressor{seen: map[string]time.Time{}, now: func() time.Time { return now }}
	window := 30 * time.Minute
	if !s.allow("p|BLOCKED|loop", window) {
		t.Fatal("the first notification must go through")
	}
	if s.allow("p|BLOCKED|loop", window) {
		t.Fatal("an identical repeat must be suppressed")
	}
	// A different reason is news even for the same project and state.
	if !s.allow("p|BLOCKED|quota", window) {
		t.Fatal("a changed reason must go through")
	}
	now = now.Add(window + time.Second)
	if !s.allow("p|BLOCKED|loop", window) {
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
	s := &suppressor{seen: map[string]time.Time{}, now: time.Now}
	for i := 0; i < 3; i++ {
		if !s.allow("p|BLOCKED|loop", 0) {
			t.Fatal("a zero window must not suppress")
		}
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
