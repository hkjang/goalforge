package main

import (
	"testing"
	"time"
)

// Go's own parser stops at hours, so "30d" — the unit anyone reaches for when
// talking about how long to keep logs — was a parse error, including in this
// program's own help text.
func TestDurationAcceptsDaysAndWeeks(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"30d":  30 * 24 * time.Hour,
		"1d":   24 * time.Hour,
		"12w":  12 * 7 * 24 * time.Hour,
		"720h": 720 * time.Hour,
		"90m":  90 * time.Minute,
		"1.5d": 36 * time.Hour,
	} {
		got, err := dayDuration(value)
		if err != nil {
			t.Errorf("%s: %v", value, err)
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %v", value, got, want)
		}
	}
}

func TestUnreadableDurationSaysWhatItAccepts(t *testing.T) {
	for _, value := range []string{"", "soon", "30 days", "d"} {
		_, err := dayDuration(value)
		if err == nil {
			t.Errorf("%q must be refused", value)
		}
	}
}
