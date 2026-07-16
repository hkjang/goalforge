package policy

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// FailureKind classifies a run failure for the retry matrix.
type FailureKind string

const (
	FailureAccountQuota     FailureKind = "account_quota"
	FailureRateLimit        FailureKind = "rate_limit"
	FailureOverload         FailureKind = "overload"
	FailureNetwork          FailureKind = "network"
	FailureAuthExpired      FailureKind = "auth_expired"
	FailureModelUnsupported FailureKind = "model_unsupported"
	FailureSessionCorrupt   FailureKind = "session_corrupt"
	FailureGitConflict      FailureKind = "git_conflict"
	FailureUnknown          FailureKind = "unknown"
)

// RetryAction is what the orchestrator should do about a classified failure.
type RetryAction string

const (
	RetryAfterDelay    RetryAction = "RETRY_AFTER_DELAY"
	WaitQuotaReset     RetryAction = "WAIT_QUOTA_RESET"
	NewSessionFromCkpt RetryAction = "NEW_SESSION_FROM_CHECKPOINT"
	UseFallbackModel   RetryAction = "USE_FALLBACK_MODEL"
	BlockForUser       RetryAction = "BLOCK_FOR_USER"
)

type RetryDecision struct {
	Action RetryAction
	Delay  time.Duration
	Reason string
}

// backoffSteps is the recommended exponential ladder. It is never applied to
// account-quota exhaustion: polling an exhausted account only burns usage.
var backoffSteps = [...]time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

// Backoff returns the delay before retry attempt (1-based) with up to 20%
// added jitter. Attempts beyond the ladder reuse the final step. jitter must
// return a value in [0,1); nil disables jitter for deterministic callers.
func Backoff(attempt int, jitter func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(backoffSteps) {
		attempt = len(backoffSteps)
	}
	base := backoffSteps[attempt-1]
	if jitter == nil {
		return base
	}
	return base + time.Duration(jitter()*0.2*float64(base))
}

// ClassifyFailure maps an error and its raw provider message onto the retry
// matrix. Callers translate their own sentinel errors (quota waits, invalid
// sessions) before falling back to this text-based classification.
func ClassifyFailure(err error, rawMessage string) FailureKind {
	text := strings.ToLower(rawMessage)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) {
			return FailureNetwork
		}
		if text == "" {
			text = strings.ToLower(err.Error())
		} else {
			text += " " + strings.ToLower(err.Error())
		}
	}
	switch {
	case containsAny(text, "usage limit", "session limit", "weekly limit", "hit your", "quota exhausted", "quota exceeded"):
		return FailureAccountQuota
	case containsAny(text, "rate limit", "too many requests", "429"):
		return FailureRateLimit
	case containsAny(text, "overloaded", "server overload", "503", "529", "service unavailable", "internal server error"):
		return FailureOverload
	case containsAny(text, "connection refused", "connection reset", "no such host", "network is unreachable", "dial tcp", "broken pipe", "eof"):
		return FailureNetwork
	case containsAny(text, "unauthorized", "401", "authentication", "invalid api key", "token expired", "login required", "credential"):
		return FailureAuthExpired
	case containsAny(text, "model not found", "unknown model", "unsupported model", "invalid model", "no such model"):
		return FailureModelUnsupported
	case containsAny(text, "session corrupt", "session not found", "no conversation found", "session expired", "resume failed"):
		return FailureSessionCorrupt
	case containsAny(text, "merge conflict", "needs merge", "would be overwritten by", "unmerged"):
		return FailureGitConflict
	default:
		return FailureUnknown
	}
}

// DecideRetry applies the retry matrix: account quota waits for reset without
// polling, Retry-After wins for short rate limits, transient failures use the
// exponential ladder up to maxAttempts, and everything requiring judgment
// (auth, git conflicts, exhausted retries) blocks for the user. Verification
// failures never reach this function - they become repair work items, not
// retries.
func DecideRetry(kind FailureKind, attempt, maxAttempts int, retryAfter *time.Duration, jitter func() float64) RetryDecision {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	switch kind {
	case FailureAccountQuota:
		return RetryDecision{Action: WaitQuotaReset, Reason: "account quota exhausted; wait for the reset time, do not poll"}
	case FailureRateLimit:
		if retryAfter != nil && *retryAfter > 0 {
			return RetryDecision{Action: RetryAfterDelay, Delay: *retryAfter, Reason: "short rate limit; honoring Retry-After"}
		}
		return RetryDecision{Action: RetryAfterDelay, Delay: Backoff(attempt, jitter), Reason: "short rate limit without Retry-After; exponential backoff"}
	case FailureOverload:
		if attempt >= maxAttempts {
			return RetryDecision{Action: BlockForUser, Reason: "provider still overloaded after maximum retries"}
		}
		return RetryDecision{Action: RetryAfterDelay, Delay: Backoff(attempt, jitter), Reason: "provider overloaded; exponential backoff with jitter"}
	case FailureNetwork:
		if attempt >= maxAttempts {
			return RetryDecision{Action: BlockForUser, Reason: "network failure persisted past retry limit"}
		}
		return RetryDecision{Action: RetryAfterDelay, Delay: Backoff(attempt, jitter), Reason: "transient network failure; limited retries"}
	case FailureAuthExpired:
		return RetryDecision{Action: BlockForUser, Reason: "authentication expired; renew credentials before continuing"}
	case FailureModelUnsupported:
		return RetryDecision{Action: UseFallbackModel, Reason: "model unavailable; switch to an approved fallback model"}
	case FailureSessionCorrupt:
		return RetryDecision{Action: NewSessionFromCkpt, Reason: "session unusable; start a new session from the latest checkpoint"}
	case FailureGitConflict:
		return RetryDecision{Action: BlockForUser, Reason: "git conflict requires user review; automatic merges are forbidden"}
	default:
		if attempt >= maxAttempts {
			return RetryDecision{Action: BlockForUser, Reason: "same failure repeated past retry limit"}
		}
		return RetryDecision{Action: RetryAfterDelay, Delay: Backoff(attempt, jitter), Reason: "unclassified failure; conservative backoff"}
	}
}

// WaitForRetry sleeps for decision.Delay unless ctx ends first.
func WaitForRetry(ctx context.Context, decision RetryDecision) error {
	if decision.Delay <= 0 {
		return nil
	}
	timer := time.NewTimer(decision.Delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
