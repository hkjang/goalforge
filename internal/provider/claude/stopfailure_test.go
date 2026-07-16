package claude

import (
	"testing"
	"time"
)

func TestDecodeStopFailureRateLimitAndReset(t *testing.T) {
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	input := `{"session_id":"s1","hook_event_name":"StopFailure","error":"rate_limit","error_details":"429","last_assistant_message":"You've hit your session limit; resets at 3:00 PM"}`
	failure, quota, err := DecodeStopFailure([]byte(input), now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 16, 15, 0, 0, 0, now.Location()).UTC()
	if failure.Error != "rate_limit" || !quota.LimitReached || quota.ResetAt == nil || !quota.ResetAt.Equal(want) || quota.Confidence != "high" {
		t.Fatalf("failure=%+v quota=%+v", failure, quota)
	}
}

func TestParseResetTimeRFC3339AndRetryDuration(t *testing.T) {
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	if got := ParseResetTime("resets 2026-07-16T15:00:00+09:00", now); got == nil || !got.Equal(time.Date(2026, 7, 16, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("RFC3339 reset=%v", got)
	}
	if got := ParseResetTime("try again in 1 hour 30 minutes", now); got == nil || !got.Equal(now.Add(90*time.Minute)) {
		t.Fatalf("duration reset=%v", got)
	}
}
