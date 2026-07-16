package usage

import (
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/provider"
)

func TestDefaultThresholds(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct {
		used float64
		want Action
	}{{79, ActionAllow}, {80, ActionWarn}, {90, ActionDrain}, {97, ActionBlock}}
	for _, tc := range cases {
		if got := p.Evaluate(time.Now(), provider.QuotaSnapshot{UsedPercent: tc.used}, Budget{}).Action; got != tc.want {
			t.Fatalf("used %.0f: got %s want %s", tc.used, got, tc.want)
		}
	}
}
func TestQuotaResetAndResumeAreSeparated(t *testing.T) {
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	reset := now.Add(5 * time.Hour)
	d := DefaultPolicy().Evaluate(now, provider.QuotaSnapshot{LimitReached: true, ResetAt: &reset}, Budget{})
	if d.Action != ActionWaitQuota || d.QuotaResetAt == nil || !d.QuotaResetAt.Equal(reset) || d.ResumeAt == nil || !d.ResumeAt.Equal(reset.Add(time.Minute)) {
		t.Fatalf("decision=%+v", d)
	}
}
func TestUnknownResetBlocks(t *testing.T) {
	d := DefaultPolicy().Evaluate(time.Now(), provider.QuotaSnapshot{LimitReached: true}, Budget{})
	if d.Action != ActionBlock {
		t.Fatalf("decision=%+v", d)
	}
}
func TestProjectBudgetsAreIndependent(t *testing.T) {
	p := DefaultPolicy()
	if got := p.Evaluate(time.Now(), provider.QuotaSnapshot{}, Budget{TokenLimit: 100, TokensUsed: 100}).Action; got != ActionBudgetExceeded {
		t.Fatalf("token action=%s", got)
	}
	if got := p.Evaluate(time.Now(), provider.QuotaSnapshot{}, Budget{CostLimitUSD: 1, CostUsedUSD: 1}).Action; got != ActionBudgetExceeded {
		t.Fatalf("cost action=%s", got)
	}
}
