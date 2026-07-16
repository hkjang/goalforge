package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func (o *Orchestrator) handleRuntimeQuotaFailure(ctx context.Context, p provider.Provider, request Request, result Result) (bool, Result, error) {
	quota, err := p.GetQuota(ctx, provider.AccountRef{ID: "default"})
	if err != nil || !quota.LimitReached {
		return false, result, nil
	}
	now := time.Now().UTC()
	snapshot, err := o.inspector.Snapshot(ctx, request.Project.RepositoryPath)
	if err != nil {
		return true, result, err
	}
	goal, err := o.store.CurrentGoal(ctx, request.Project.ID)
	if err != nil {
		return true, result, err
	}
	checkpoint := store.Checkpoint{ProjectID: request.Project.ID, RunID: request.RunID, GoalVersion: goal.Version, WorkItemID: request.WorkItemID, Provider: request.Project.Provider, Model: request.Project.Model, SessionID: result.SessionID, CommitSHA: snapshot.CommitSHA, Branch: snapshot.Branch, DirtyFiles: snapshot.DirtyFiles, DirtyFingerprint: snapshot.DirtyFingerprint, CompletedSummary: result.FinalMessage, RemainingSteps: "provider turn failed because quota was exhausted", NextAction: "resume the same work item after provider quota is available"}
	window := quotaWindow(quota, now, "exhausted", nil)
	if quota.ResetAt == nil {
		if err = o.store.FinishRun(ctx, request.RunID, "DRAINING", "CHECKPOINTING"); err != nil {
			return true, result, err
		}
		if _, err = o.store.CreateCheckpoint(ctx, checkpoint); err != nil {
			return true, result, err
		}
		if err = o.store.UpsertQuotaWindow(ctx, window); err != nil {
			return true, result, err
		}
		if err = o.store.TransitionProjectState(ctx, request.Project.ID, "CHECKPOINTING", "BLOCKED"); err != nil {
			return true, result, err
		}
		result.State = "BLOCKED"
		result.Usage, _ = o.store.RunUsage(ctx, request.RunID)
		return true, result, errors.Join(ErrPreflightBlocked, errors.New("provider quota exhausted without a known reset time"))
	}
	resumeAt := quota.ResetAt.Add(o.quotaPolicy.SafetyDelay)
	window.QuotaResetAt, window.ResumeAt = quota.ResetAt, &resumeAt
	payload, _ := json.Marshal(map[string]string{"account_id": "default", "limit_type": quota.LimitType})
	job := store.SchedulerJob{ProjectID: request.Project.ID, Type: "RESUME", RunAt: resumeAt, IdempotencyKey: fmt.Sprintf("quota:%s:%s:%s", request.Project.ID, p.Name(), quota.LimitType), Payload: string(payload)}
	if err = o.store.EnterQuotaWait(ctx, window, checkpoint, job); err != nil {
		return true, result, err
	}
	result.State = "WAITING_QUOTA"
	result.Usage, _ = o.store.RunUsage(ctx, request.RunID)
	return true, result, errors.Join(ErrWaitingQuota, errors.New("provider quota exhausted during run"))
}
