package usage

import (
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/provider"
)

type Action string

const (
	ActionAllow          Action = "ALLOW"
	ActionWarn           Action = "WARN"
	ActionDrain          Action = "DRAIN"
	ActionBlock          Action = "BLOCK"
	ActionWaitQuota      Action = "WAIT_QUOTA"
	ActionBudgetExceeded Action = "BUDGET_EXCEEDED"
)

type Policy struct {
	WarnAt, DrainAt, BlockAt float64
	SafetyDelay              time.Duration
}

func DefaultPolicy() Policy {
	return Policy{WarnAt: 80, DrainAt: 90, BlockAt: 97, SafetyDelay: time.Minute}
}

type Budget struct {
	TokenLimit   int64
	CostLimitUSD float64
	TokensUsed   int64
	CostUsedUSD  float64
}
type Decision struct {
	Action                 Action
	QuotaResetAt, ResumeAt *time.Time
	Reason                 string
}

func (p Policy) Evaluate(now time.Time, quota provider.QuotaSnapshot, budget Budget) Decision {
	if budget.TokenLimit > 0 && budget.TokensUsed >= budget.TokenLimit {
		return Decision{Action: ActionBudgetExceeded, Reason: "project token budget exhausted"}
	}
	if budget.CostLimitUSD > 0 && budget.CostUsedUSD >= budget.CostLimitUSD {
		return Decision{Action: ActionBudgetExceeded, Reason: "project cost budget exhausted"}
	}
	if quota.LimitReached || quota.UsedPercent >= 100 {
		if quota.ResetAt == nil {
			return Decision{Action: ActionBlock, Reason: "provider quota exhausted without a known reset time"}
		}
		resume := quota.ResetAt.Add(p.SafetyDelay)
		return Decision{Action: ActionWaitQuota, QuotaResetAt: quota.ResetAt, ResumeAt: &resume, Reason: "provider quota exhausted"}
	}
	switch {
	case quota.UsedPercent >= p.BlockAt:
		return Decision{Action: ActionBlock, Reason: "provider quota reached call-block threshold"}
	case quota.UsedPercent >= p.DrainAt:
		return Decision{Action: ActionDrain, Reason: "provider quota reached drain threshold"}
	case quota.UsedPercent >= p.WarnAt:
		return Decision{Action: ActionWarn, Reason: "provider quota reached warning threshold"}
	default:
		return Decision{Action: ActionAllow}
	}
}

func (p Policy) Validate() error {
	if p.WarnAt < 0 || p.WarnAt >= p.DrainAt || p.DrainAt >= p.BlockAt || p.BlockAt >= 100 {
		return errors.New("thresholds must satisfy 0 <= warn < drain < block < 100")
	}
	if p.SafetyDelay < 0 {
		return errors.New("safety delay cannot be negative")
	}
	return nil
}
