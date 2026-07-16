package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/scheduler"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/usage"
)

type ResumeConfig struct {
	Owner         string
	LeaseDuration time.Duration
	Policy        usage.Policy
	Inspector     gitops.Inspector
	Now           func() time.Time
	NewRunID      func() string
}
type resumePayload struct {
	AccountID string `json:"account_id"`
	LimitType string `json:"limit_type"`
}

func (o *Orchestrator) ResumeHandler(config ResumeConfig) scheduler.Handler {
	return func(ctx context.Context, job store.SchedulerJob) (out scheduler.Outcome, err error) {
		if config.Now == nil {
			config.Now = time.Now
		}
		if config.NewRunID == nil {
			config.NewRunID = func() string { return store.NewID("RUN") }
		}
		if config.Policy.WarnAt == 0 {
			config.Policy = usage.DefaultPolicy()
		}
		if config.Inspector == nil {
			return out, errors.New("Git inspector is required")
		}
		now := config.Now().UTC()
		if err = o.store.AcquireLease(ctx, job.ProjectID, config.Owner, now, config.LeaseDuration); err != nil {
			return out, err
		}
		defer func() {
			releaseErr := o.store.ReleaseLease(context.WithoutCancel(ctx), job.ProjectID, config.Owner)
			err = errors.Join(err, releaseErr)
		}()
		project, err := o.store.ProjectByID(ctx, job.ProjectID)
		if err != nil {
			return out, err
		}
		if project.State != "WAITING_QUOTA" {
			return out, fmt.Errorf("project state is %s, expected WAITING_QUOTA", project.State)
		}
		p := o.providers[project.Provider]
		if p == nil {
			return out, fmt.Errorf("provider %q is not registered", project.Provider)
		}
		var payload resumePayload
		if job.Payload != "" {
			if err = json.Unmarshal([]byte(job.Payload), &payload); err != nil {
				return out, fmt.Errorf("decode resume payload: %w", err)
			}
		}
		if payload.AccountID == "" {
			payload.AccountID = "default"
		}
		quota, err := p.GetQuota(ctx, provider.AccountRef{ID: payload.AccountID})
		if err != nil {
			_ = o.store.TransitionProjectState(ctx, project.ID, "WAITING_QUOTA", "BLOCKED")
			return out, fmt.Errorf("quota recheck required before resume: %w", err)
		}
		budget := usage.Budget{}
		if stored, budgetErr := o.store.ProjectBudgetUsage(ctx, project.ID); budgetErr == nil {
			budget = usage.Budget{TokenLimit: stored.TokenLimit, CostLimitUSD: stored.CostLimitUSD, TokensUsed: stored.TokensUsed, CostUsedUSD: stored.CostUsedUSD}
		} else if !errors.Is(budgetErr, store.ErrNotFound) {
			return out, budgetErr
		}
		decision := config.Policy.Evaluate(now, quota, budget)
		if decision.Action == usage.ActionWaitQuota || decision.Action == usage.ActionDrain || decision.Action == usage.ActionBlock {
			resumeAt := decision.ResumeAt
			if resumeAt == nil && quota.ResetAt != nil {
				value := quota.ResetAt.Add(config.Policy.SafetyDelay)
				resumeAt = &value
			}
			if resumeAt != nil {
				out.RescheduleAt = resumeAt
				_ = o.store.UpsertQuotaWindow(ctx, store.QuotaWindow{Provider: project.Provider, AccountID: payload.AccountID, LimitType: quota.LimitType, Status: "limited", UsedPercent: quota.UsedPercent, DetectedAt: now, QuotaResetAt: quota.ResetAt, ResumeAt: resumeAt, Source: quota.Source, Confidence: quota.Confidence, RawMessage: quota.RawMessage})
				return out, fmt.Errorf("quota still unavailable: %s", decision.Reason)
			}
			_ = o.store.TransitionProjectState(ctx, project.ID, "WAITING_QUOTA", "BLOCKED")
			return out, errors.New(decision.Reason)
		}
		if decision.Action == usage.ActionBudgetExceeded {
			_ = o.store.TransitionProjectState(ctx, project.ID, "WAITING_QUOTA", "BLOCKED")
			return out, errors.New(decision.Reason)
		}
		checkpoint, err := o.store.LatestCheckpoint(ctx, project.ID)
		if err != nil {
			return out, err
		}
		if checkpoint.WorkItemID != "" {
			worktree, worktreeErr := o.store.WorktreeForWorkItem(ctx, project.ID, checkpoint.WorkItemID)
			if worktreeErr == nil {
				project.RepositoryPath = worktree.Path
			} else if project.WorktreeEnabled || !errors.Is(worktreeErr, store.ErrNotFound) {
				_ = o.store.TransitionProjectState(ctx, project.ID, "WAITING_QUOTA", "BLOCKED")
				return out, fmt.Errorf("load checkpoint worktree: %w", worktreeErr)
			}
		}
		current, err := config.Inspector.Snapshot(ctx, project.RepositoryPath)
		if err != nil {
			return out, err
		}
		saved := gitops.Snapshot{CommitSHA: checkpoint.CommitSHA, Branch: checkpoint.Branch, DirtyFiles: checkpoint.DirtyFiles, DirtyFingerprint: checkpoint.DirtyFingerprint}
		if err = gitops.EqualSnapshot(saved, current); err != nil {
			_ = o.store.TransitionProjectState(ctx, project.ID, "WAITING_QUOTA", "BLOCKED")
			return out, fmt.Errorf("repository changed while waiting: %w", err)
		}
		if checkpoint.SessionID != "" {
			session, sessionErr := o.store.ActiveSession(ctx, project.ID, project.Provider)
			if sessionErr != nil || session.SessionID != checkpoint.SessionID {
				_ = o.store.TransitionProjectState(ctx, project.ID, "WAITING_QUOTA", "BLOCKED")
				return out, errors.New("saved provider session no longer matches checkpoint")
			}
		}
		if err = o.store.TransitionProjectState(ctx, project.ID, "WAITING_QUOTA", "RESUMING"); err != nil {
			return out, err
		}
		prompt := BuildResumePrompt(checkpoint)
		_, err = o.Run(ctx, Request{RunID: config.NewRunID(), WorkItemID: checkpoint.WorkItemID, Prompt: prompt, PromptTemplate: "quota_resume", TaskType: model.TaskContinueGoal, Project: project, WorkspaceWrite: true})
		return out, err
	}
}

func BuildResumePrompt(c store.Checkpoint) string {
	return strings.TrimSpace(fmt.Sprintf(`현재 프로젝트 목표와 기존 세션의 작업을 계속 수행하라.

재개 사유:
사용량 한도 도달로 이전 실행이 안전하게 중단되었다.

현재 작업: %s
이전 실행 완료 내역: %s
검증 상태: %s
남은 작업: %s
다음 행동: %s

반드시 먼저 현재 저장소 상태와 이전 변경 사항을 확인하라.
이미 완료된 작업을 반복하지 말고 다음 미완료 단계부터 진행하라.
한 번에 하나의 명확한 작업만 수행하고 관련 테스트를 실행하라.`, c.WorkItemID, c.CompletedSummary, c.VerificationSummary, c.RemainingSteps, c.NextAction))
}
