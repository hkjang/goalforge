package policy

import (
	"errors"
	"testing"
	"time"
)

func TestBackoffFollowsLadderAndCapsAtFinalStep(t *testing.T) {
	expected := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for attempt, want := range expected {
		if got := Backoff(attempt+1, nil); got != want {
			t.Fatalf("attempt %d: got %s want %s", attempt+1, got, want)
		}
	}
	if got := Backoff(0, nil); got != 30*time.Second {
		t.Fatalf("attempt 0 clamped: %s", got)
	}
	jittered := Backoff(1, func() float64 { return 0.5 })
	if jittered != 33*time.Second {
		t.Fatalf("jittered=%s", jittered)
	}
}

func TestClassifyFailureMatrix(t *testing.T) {
	cases := []struct {
		message string
		want    FailureKind
	}{
		{"You've hit your session limit", FailureAccountQuota},
		{"weekly limit reached", FailureAccountQuota},
		{"429 Too Many Requests", FailureRateLimit},
		{"upstream 529 overloaded", FailureOverload},
		{"dial tcp 10.0.0.1:443: connection refused", FailureNetwork},
		{"401 unauthorized: token expired", FailureAuthExpired},
		{"unknown model claude-nonexistent", FailureModelUnsupported},
		{"no conversation found with session id", FailureSessionCorrupt},
		{"error: your local changes would be overwritten by merge", FailureGitConflict},
		{"something novel happened", FailureUnknown},
	}
	for _, c := range cases {
		if got := ClassifyFailure(nil, c.message); got != c.want {
			t.Fatalf("%q: got %s want %s", c.message, got, c.want)
		}
	}
	if got := ClassifyFailure(errors.New("rate limit reached"), ""); got != FailureRateLimit {
		t.Fatalf("error-only classification: %s", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		message string
		want    time.Duration
	}{
		{"429 Too Many Requests. Retry-After: 42", 42 * time.Second},
		{"rate limited, retry-after=90", 90 * time.Second},
		{"please retry in 30 seconds", 30 * time.Second},
		{"overloaded, try again in 2 minutes", 2 * time.Minute},
		{"rate limit reached, retry after 1 hour", time.Hour},
		{"wait 15 secs before retrying", 15 * time.Second},
		{"rate limited, retry-after=3600", time.Hour},
	}
	for _, c := range cases {
		got := ParseRetryAfter(c.message)
		if got == nil || *got != c.want {
			t.Fatalf("%q: got %v want %s", c.message, got, c.want)
		}
	}
	for _, message := range []string{"generic failure", "retry later", "wait for the reset"} {
		if got := ParseRetryAfter(message); got != nil {
			t.Fatalf("%q: expected nil, got %s", message, *got)
		}
	}
}

// TestParseRetryAfterRefusesHintsPastTheShortWaitCap pins the upper bound the
// doc comment promises: a hint an unattended worker would sleep through is no
// longer a hint, so the caller must fall back to the backoff ladder.
func TestParseRetryAfterRefusesHintsPastTheShortWaitCap(t *testing.T) {
	for _, message := range []string{
		"429 Too Many Requests. Retry-After: 86400",
		"rate limited, retry-after=3601",
		"rate limit reached, retry after 2 hours",
		"please wait 90 minutes",
		"retry in 99999999999 hours",
		"try again in 7200 seconds",
	} {
		if got := ParseRetryAfter(message); got != nil {
			t.Fatalf("%q: expected nil past the short-wait cap, got %s", message, *got)
		}
	}
}

// TestLongRateLimitHintCannotParkTheUnattendedWorker composes the parse and the
// decision exactly as cmd/goalforge/main.go does for run --loop, because the
// defect only matters through that wiring: a provider line is all it takes for
// the worker to sleep past any plausible supervision window.
func TestLongRateLimitHintCannotParkTheUnattendedWorker(t *testing.T) {
	for _, message := range []string{
		"429 Too Many Requests. Retry-After: 86400",
		"429 rate limit; retry in 99999999999 hours",
	} {
		kind := ClassifyFailure(errors.New(message), "")
		if kind != FailureRateLimit {
			t.Fatalf("%q classified as %s, want %s", message, kind, FailureRateLimit)
		}
		decision := DecideRetry(kind, 1, 3, ParseRetryAfter(message), nil)
		if decision.Action != RetryAfterDelay {
			t.Fatalf("%q: action=%s want %s", message, decision.Action, RetryAfterDelay)
		}
		if decision.Delay <= 0 || decision.Delay > 10*time.Minute {
			t.Fatalf("%q: delay=%s want a backoff ladder step within 10m", message, decision.Delay)
		}
	}
}

func TestDecideRetryMatrix(t *testing.T) {
	if d := DecideRetry(FailureAccountQuota, 1, 3, nil, nil); d.Action != WaitQuotaReset || d.Delay != 0 {
		t.Fatalf("quota decision=%+v", d)
	}
	retryAfter := 42 * time.Second
	if d := DecideRetry(FailureRateLimit, 1, 3, &retryAfter, nil); d.Action != RetryAfterDelay || d.Delay != retryAfter {
		t.Fatalf("rate-limit decision=%+v", d)
	}
	if d := DecideRetry(FailureRateLimit, 2, 3, nil, nil); d.Action != RetryAfterDelay || d.Delay != time.Minute {
		t.Fatalf("rate-limit backoff decision=%+v", d)
	}
	if d := DecideRetry(FailureOverload, 3, 3, nil, nil); d.Action != BlockForUser {
		t.Fatalf("overload exhausted decision=%+v", d)
	}
	if d := DecideRetry(FailureNetwork, 1, 3, nil, nil); d.Action != RetryAfterDelay || d.Delay != 30*time.Second {
		t.Fatalf("network decision=%+v", d)
	}
	if d := DecideRetry(FailureAuthExpired, 1, 3, nil, nil); d.Action != BlockForUser {
		t.Fatalf("auth decision=%+v", d)
	}
	if d := DecideRetry(FailureModelUnsupported, 1, 3, nil, nil); d.Action != UseFallbackModel {
		t.Fatalf("model decision=%+v", d)
	}
	if d := DecideRetry(FailureSessionCorrupt, 1, 3, nil, nil); d.Action != NewSessionFromCkpt {
		t.Fatalf("session decision=%+v", d)
	}
	if d := DecideRetry(FailureGitConflict, 1, 3, nil, nil); d.Action != BlockForUser {
		t.Fatalf("git decision=%+v", d)
	}
	if d := DecideRetry(FailureUnknown, 3, 3, nil, nil); d.Action != BlockForUser {
		t.Fatalf("unknown exhausted decision=%+v", d)
	}
}
