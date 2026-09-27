package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/goalforge/goalforge/internal/policy"
)

// RepairPolicy bounds automatic repair. Without a ceiling a failing work item
// can consume a budget one retry at a time while making no progress.
type RepairPolicy struct {
	MaxAttempts int
	MaxCostUSD  float64
}

func DefaultRepairPolicy() RepairPolicy {
	return RepairPolicy{MaxAttempts: 2, MaxCostUSD: 5}
}

// Repair decisions.
const (
	RepairRetry           = "RETRY_CODE_FIX"
	RepairBlockEnv        = "BLOCK_ENVIRONMENT"
	RepairBlockHuman      = "BLOCK_FOR_USER"
	RepairBlockAttempts   = "BLOCK_ATTEMPT_LIMIT"
	RepairBlockCost       = "BLOCK_COST_LIMIT"
	RepairNothingToRepair = "NOTHING_TO_REPAIR"
)

// RepairPlan is the decision about a failed run: what failed, what would
// address it, and whether GoalForge may try again on its own.
type RepairPlan struct {
	RunID, ProjectID, WorkItemID string
	FailureKind, RepairMode      string
	Decision, Reason, Summary    string
	Attempt                      int
	SpentUSD                     float64
	CreatedAt                    time.Time
}

// Automatic reports whether the plan lets GoalForge retry without a person.
func (p RepairPlan) Automatic() bool { return p.Decision == RepairRetry }

// PlanRepair classifies a failed run and decides whether to retry it. An
// environment or credential failure never becomes a retry: re-running a model
// against a missing binary only spends budget. Code-fix retries are capped by
// attempts and by the cost already spent repairing the same work item.
func (s *Store) PlanRepair(ctx context.Context, runID string, repairPolicy RepairPolicy) (RepairPlan, error) {
	if repairPolicy.MaxAttempts <= 0 {
		repairPolicy.MaxAttempts = DefaultRepairPolicy().MaxAttempts
	}
	if repairPolicy.MaxCostUSD <= 0 {
		repairPolicy.MaxCostUSD = DefaultRepairPolicy().MaxCostUSD
	}
	plan := RepairPlan{RunID: runID, CreatedAt: time.Now().UTC()}
	err := s.db.QueryRowContext(ctx, `SELECT project_id,COALESCE(work_item_id,'') FROM runs WHERE id=?`, runID).Scan(&plan.ProjectID, &plan.WorkItemID)
	if errors.Is(err, sql.ErrNoRows) {
		return plan, ErrNotFound
	}
	if err != nil {
		return plan, err
	}
	// The blocking failure decides the plan, and the least automatable mode
	// wins: one broken environment makes the whole run unfixable by code.
	rows, err := s.db.QueryContext(ctx, `SELECT check_type,failure_kind,repair_mode FROM verification_results WHERE run_id=? AND required=1 AND status<>'PASSED' ORDER BY id`, runID)
	if err != nil {
		return plan, err
	}
	defer rows.Close()
	rank := map[string]int{string(policy.RepairCodeFix): 0, string(policy.RepairEnvironment): 1, string(policy.RepairHuman): 2}
	worst := -1
	for rows.Next() {
		var checkType, kind, mode string
		if err = rows.Scan(&checkType, &kind, &mode); err != nil {
			return plan, err
		}
		if rank[mode] > worst {
			worst, plan.FailureKind, plan.RepairMode = rank[mode], kind, mode
			plan.Summary = fmt.Sprintf("%s 게이트 실패: %s", checkType, policy.ClassifyGateFailureSummary(kind))
		}
	}
	if err = rows.Err(); err != nil {
		return plan, err
	}
	if worst < 0 {
		plan.Decision, plan.Reason = RepairNothingToRepair, "실패한 필수 게이트가 없습니다"
		return plan, nil
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(cost_usd),0) FROM repair_attempts WHERE project_id=? AND work_item_id=? AND decision=?`, plan.ProjectID, plan.WorkItemID, RepairRetry).Scan(&plan.Attempt, &plan.SpentUSD); err != nil {
		return plan, err
	}
	plan.Attempt++
	runCost, err := s.runCost(ctx, runID)
	if err != nil {
		return plan, err
	}
	switch {
	case plan.RepairMode == string(policy.RepairEnvironment):
		plan.Decision, plan.Reason = RepairBlockEnv, "환경 문제는 모델 재실행으로 해결되지 않습니다"
	case plan.RepairMode == string(policy.RepairHuman):
		plan.Decision, plan.Reason = RepairBlockHuman, "자동으로 판단할 수 없어 사람의 확인이 필요합니다"
	case plan.WorkItemID == "":
		plan.Decision, plan.Reason = RepairBlockHuman, "작업 항목이 없는 실행은 자동 복구 대상이 아닙니다"
	case plan.Attempt > repairPolicy.MaxAttempts:
		plan.Decision, plan.Reason = RepairBlockAttempts, fmt.Sprintf("자동 수정 %d회 한도에 도달했습니다", repairPolicy.MaxAttempts)
	case plan.SpentUSD+runCost > repairPolicy.MaxCostUSD:
		plan.Decision, plan.Reason = RepairBlockCost, fmt.Sprintf("복구 비용 한도 $%.2f 를 넘습니다 (사용 $%.2f)", repairPolicy.MaxCostUSD, plan.SpentUSD+runCost)
	default:
		plan.Decision, plan.Reason = RepairRetry, fmt.Sprintf("코드 수정으로 해결 가능한 실패이며 %d/%d 번째 시도입니다", plan.Attempt, repairPolicy.MaxAttempts)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO repair_attempts(id,project_id,work_item_id,run_id,failure_kind,repair_mode,decision,reason,attempt,cost_usd,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		NewID("RPR"), plan.ProjectID, plan.WorkItemID, runID, plan.FailureKind, plan.RepairMode, plan.Decision, plan.Reason, plan.Attempt, runCost, plan.CreatedAt.Format(time.RFC3339Nano))
	return plan, err
}

func (s *Store) runCost(ctx context.Context, runID string) (float64, error) {
	var cost float64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost),0) FROM usage_ledger WHERE run_id=?`, runID).Scan(&cost)
	return cost, err
}

// RepairPlanForRun returns the decision recorded for a run, if any, so the
// dashboard can explain what happened after a failure without recomputing it.
func (s *Store) RepairPlanForRun(ctx context.Context, runID string) (RepairPlan, error) {
	var plan RepairPlan
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT project_id,work_item_id,run_id,failure_kind,repair_mode,decision,reason,attempt,cost_usd,created_at FROM repair_attempts WHERE run_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, runID).
		Scan(&plan.ProjectID, &plan.WorkItemID, &plan.RunID, &plan.FailureKind, &plan.RepairMode, &plan.Decision, &plan.Reason, &plan.Attempt, &plan.SpentUSD, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return plan, ErrNotFound
	}
	if err == nil {
		plan.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		plan.Summary = policy.ClassifyGateFailureSummary(plan.FailureKind)
	}
	return plan, err
}
