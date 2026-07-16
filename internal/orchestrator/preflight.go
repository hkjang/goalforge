package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/usage"
)

func (o *Orchestrator) preflight(ctx context.Context, p provider.Provider, request Request) (string, error) {
	now := time.Now().UTC()
	if limits, daily, dailyErr := o.store.ProjectDailyUsage(ctx, request.Project.ID, now); dailyErr == nil {
		reason := ""
		switch {
		case limits.DailyRunLimit > 0 && daily.Runs >= limits.DailyRunLimit:
			reason = fmt.Sprintf("daily run limit exhausted: %d/%d", daily.Runs, limits.DailyRunLimit)
		case limits.DailyTokenLimit > 0 && daily.Tokens >= limits.DailyTokenLimit:
			reason = fmt.Sprintf("daily token limit exhausted: %d/%d", daily.Tokens, limits.DailyTokenLimit)
		case limits.DailyCostLimitUSD > 0 && daily.CostUSD >= limits.DailyCostLimitUSD:
			reason = fmt.Sprintf("daily cost limit exhausted: %.4f/%.4f USD", daily.CostUSD, limits.DailyCostLimitUSD)
		}
		if reason != "" {
			reset := daily.DayStart.Add(24 * time.Hour)
			window := store.QuotaWindow{Provider: p.Name(), AccountID: "project", LimitType: "daily_budget", Status: "budget_exhausted", UsedPercent: 100, DetectedAt: now, QuotaResetAt: &reset, ResumeAt: &reset, Source: "project_policy", Confidence: "high", RawMessage: reason}
			blockErr := o.store.BlockBeforeRun(ctx, request.Project.ID, request.WorkItemID, window, reason)
			return "BLOCKED", errors.Join(ErrDailyLimitExceeded, errors.New(reason), blockErr)
		}
	} else if !errors.Is(dailyErr, store.ErrNotFound) {
		return "FAILED", dailyErr
	}
	quota, err := p.GetQuota(ctx, provider.AccountRef{ID: "default"})
	if errors.Is(err, provider.ErrQuotaUnavailable) {
		return "", nil
	}
	if err != nil {
		window := store.QuotaWindow{Provider: p.Name(), AccountID: "default", LimitType: "unknown", Status: "unknown", DetectedAt: time.Now().UTC(), Source: "provider_error", Confidence: "none", RawMessage: err.Error()}
		blockErr := o.store.BlockBeforeRun(ctx, request.Project.ID, request.WorkItemID, window, "provider quota could not be checked")
		return "BLOCKED", errors.Join(ErrPreflightBlocked, err, blockErr)
	}
	if quota.Provider == "" {
		quota.Provider = p.Name()
	}
	if quota.LimitType == "" {
		quota.LimitType = "unknown"
	}
	budget := usage.Budget{}
	if stored, budgetErr := o.store.ProjectBudgetUsage(ctx, request.Project.ID); budgetErr == nil {
		budget = usage.Budget{TokenLimit: stored.TokenLimit, TokensUsed: stored.TokensUsed, CostLimitUSD: stored.CostLimitUSD, CostUsedUSD: stored.CostUsedUSD}
	} else if !errors.Is(budgetErr, store.ErrNotFound) {
		return "FAILED", budgetErr
	}
	decision := o.quotaPolicy.Evaluate(now, quota, budget)
	window := quotaWindow(quota, now, "available", nil)
	switch decision.Action {
	case usage.ActionAllow, usage.ActionWarn:
		if decision.Action == usage.ActionWarn {
			window.Status = "warning"
			if request.EstimatedTokens > o.largeWorkTokenThreshold {
				if err = o.store.DeferWorkForQuotaWarning(ctx, request.Project.ID, request.WorkItemID, window); err != nil {
					return "FAILED", err
				}
				return "READY", fmt.Errorf("%w: estimated %d tokens exceeds %d token threshold", ErrLargeWorkAtQuotaWarning, request.EstimatedTokens, o.largeWorkTokenThreshold)
			}
		}
		if err = o.store.UpsertQuotaWindow(ctx, window); err != nil {
			return "FAILED", err
		}
		return "", nil
	case usage.ActionWaitQuota, usage.ActionDrain, usage.ActionBlock:
		resetAt := decision.QuotaResetAt
		if resetAt == nil {
			resetAt = quota.ResetAt
		}
		if resetAt == nil {
			window.Status = "blocked"
			blockErr := o.store.BlockBeforeRun(ctx, request.Project.ID, request.WorkItemID, window, decision.Reason)
			return "BLOCKED", errors.Join(ErrPreflightBlocked, errors.New(decision.Reason), blockErr)
		}
		resumeAt := decision.ResumeAt
		if resumeAt == nil {
			value := resetAt.Add(o.quotaPolicy.SafetyDelay)
			resumeAt = &value
		}
		window = quotaWindow(quota, now, "limited", resumeAt)
		window.QuotaResetAt = resetAt
		checkpoint, checkpointErr := o.preflightCheckpoint(ctx, request, *resumeAt)
		if checkpointErr != nil {
			blockErr := o.store.BlockBeforeRun(ctx, request.Project.ID, request.WorkItemID, window, checkpointErr.Error())
			return "BLOCKED", errors.Join(ErrPreflightBlocked, checkpointErr, blockErr)
		}
		payload, _ := json.Marshal(map[string]string{"account_id": "default", "limit_type": quota.LimitType})
		job := store.SchedulerJob{ProjectID: request.Project.ID, Type: "RESUME", RunAt: *resumeAt, IdempotencyKey: fmt.Sprintf("quota:%s:%s:%s", request.Project.ID, p.Name(), quota.LimitType), Payload: string(payload)}
		if err = o.store.EnterQuotaWait(ctx, window, checkpoint, job); err != nil {
			return "FAILED", err
		}
		return "WAITING_QUOTA", errors.Join(ErrWaitingQuota, errors.New(decision.Reason))
	case usage.ActionBudgetExceeded:
		window.Status = "budget_exhausted"
		blockErr := o.store.BlockBeforeRun(ctx, request.Project.ID, request.WorkItemID, window, decision.Reason)
		return "BLOCKED", errors.Join(ErrPreflightBlocked, errors.New(decision.Reason), blockErr)
	default:
		return "FAILED", fmt.Errorf("unknown quota decision %s", decision.Action)
	}
}

func quotaWindow(quota provider.QuotaSnapshot, now time.Time, status string, resumeAt *time.Time) store.QuotaWindow {
	return store.QuotaWindow{Provider: quota.Provider, AccountID: "default", LimitType: quota.LimitType, Status: status, UsedPercent: quota.UsedPercent, DetectedAt: now, QuotaResetAt: quota.ResetAt, ResumeAt: resumeAt, Source: quota.Source, Confidence: quota.Confidence, RawMessage: quota.RawMessage}
}

func (o *Orchestrator) preflightCheckpoint(ctx context.Context, request Request, resumeAt time.Time) (store.Checkpoint, error) {
	snapshot, err := o.inspector.Snapshot(ctx, request.Project.RepositoryPath)
	if err != nil {
		return store.Checkpoint{}, fmt.Errorf("snapshot repository before quota wait: %w", err)
	}
	goal, err := o.store.CurrentGoal(ctx, request.Project.ID)
	if err != nil {
		return store.Checkpoint{}, err
	}
	checkpoint := store.Checkpoint{ProjectID: request.Project.ID, GoalVersion: goal.Version, WorkItemID: request.WorkItemID, Provider: request.Project.Provider, Model: request.Project.Model, CommitSHA: snapshot.CommitSHA, Branch: snapshot.Branch, DirtyFiles: snapshot.DirtyFiles, DirtyFingerprint: snapshot.DirtyFingerprint, RemainingSteps: "provider call was not started", NextAction: "start the selected work item after quota reset at " + resumeAt.Format(time.RFC3339)}
	if session, sessionErr := o.store.ActiveSession(ctx, request.Project.ID, request.Project.Provider); sessionErr == nil {
		checkpoint.SessionID = session.SessionID
	} else if !errors.Is(sessionErr, store.ErrNotFound) {
		return store.Checkpoint{}, sessionErr
	}
	return checkpoint, nil
}
