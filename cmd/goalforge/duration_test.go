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

// ParseFloat accepts "Inf", "NaN" and numbers far past what a time.Duration
// can hold, and multiplying those into an int64 is undefined rather than
// clamped: the same "1e300w" lands on MinInt64 on amd64 and on MaxInt64 on
// arm64. A positive result passes the caller's `window <= 0` check, so the
// caller stops refusing and starts reporting against a boundary in the 1700s
// that nobody asked for; the refusal belongs here, where the number is still a
// float. The assertion is deliberately only `err != nil` —
// asserting the converted value would pin an answer that differs per
// architecture, and CI runs these tests on macOS too.
func TestDurationRefusesUnrepresentableNumbers(t *testing.T) {
	for _, value := range []string{"Infd", "-Infd", "NaNd", "1e300w", "1e300d", "-1e300w"} {
		got, err := dayDuration(value)
		if err == nil {
			t.Errorf("%q must be refused, got %v (positive=%t)", value, got, got > 0)
		}
	}
}
