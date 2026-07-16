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
