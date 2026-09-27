package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

// WorkItemBlocker is a concrete reason an item cannot be started now, phrased
// so the dashboard can show why a button is disabled instead of leaving the
// user to infer it from a status code.
type WorkItemBlocker struct {
	// Kind is DEPENDENCY, WIP_LIMIT, APPROVAL, or STATUS.
	Kind, Detail string
}

// WorkItemDetail is everything needed to judge one work item: its
// specification, what is holding it up, and the runs that have attempted it.
type WorkItemDetail struct {
	Item           model.WorkItem
	Dependencies   []model.WorkItem
	Blockers       []WorkItemBlocker
	Runs           []RunView
	Commit         *RunCommit
	Score          *model.IdeaScore
	EstimateSource string
	// Forecast is the token range recent runs support, with its confidence.
	Forecast TokenForecast
	// Model is the model that would run this item and why.
	Model ModelChoice
}

func (s *Store) WorkItemByID(ctx context.Context, goalID, workID string) (model.WorkItem, error) {
	var w model.WorkItem
	err := s.db.QueryRowContext(ctx, `SELECT id,goal_id,COALESCE(milestone_id,''),type,title,priority,status,risk,change_scope,weight,estimated_tokens,objective,acceptance,blocked_reason FROM work_items WHERE id=? AND goal_id=?`, workID, goalID).
		Scan(&w.ID, &w.GoalID, &w.MilestoneID, &w.Type, &w.Title, &w.Priority, &w.Status, &w.Risk, &w.ChangeScope, &w.Weight, &w.EstimatedTokens, &w.Objective, &w.Acceptance, &w.BlockedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, err
	}
	w.Dependencies, err = s.dependenciesOf(ctx, s.db, workID)
	return w, err
}

// WorkItemDetails assembles the item with the context a decision needs. The
// blockers are computed from the same rules ClaimNextWorkItem enforces, so the
// dashboard cannot promise an action the planner would refuse.
func (s *Store) WorkItemDetails(ctx context.Context, projectID, goalID, workID string) (WorkItemDetail, error) {
	var detail WorkItemDetail
	item, err := s.WorkItemByID(ctx, goalID, workID)
	if err != nil {
		return detail, err
	}
	detail.Item = item
	for _, dependencyID := range item.Dependencies {
		dependency, depErr := s.WorkItemByID(ctx, goalID, dependencyID)
		switch {
		case errors.Is(depErr, ErrNotFound):
			detail.Blockers = append(detail.Blockers, WorkItemBlocker{Kind: "DEPENDENCY", Detail: fmt.Sprintf("선행 작업 %s 를 찾을 수 없습니다", dependencyID)})
		case depErr != nil:
			return detail, depErr
		default:
			detail.Dependencies = append(detail.Dependencies, dependency)
			if dependency.Status != "DONE" {
				detail.Blockers = append(detail.Blockers, WorkItemBlocker{Kind: "DEPENDENCY",
					Detail: fmt.Sprintf("선행 작업 %s (%s) 가 아직 %s 입니다", dependency.ID, dependency.Title, dependency.Status)})
			}
		}
	}
	var active string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE goal_id=? AND status='IN_PROGRESS' AND id<>? LIMIT 1`, goalID, workID).Scan(&active)
	if err == nil {
		detail.Blockers = append(detail.Blockers, WorkItemBlocker{Kind: "WIP_LIMIT", Detail: fmt.Sprintf("%s 가 진행 중입니다 (동시 구현 1건 제한)", active)})
	} else if !errors.Is(err, sql.ErrNoRows) {
		return detail, err
	}
	scores, err := s.IdeaScoresForGoal(ctx, goalID)
	if err != nil {
		return detail, err
	}
	if score, ok := scores[workID]; ok {
		detail.Score = &score
		if score.ApprovalRequired && item.Status != "APPROVED" {
			detail.Blockers = append(detail.Blockers, WorkItemBlocker{Kind: "APPROVAL", Detail: "범위를 확장하는 제안이라 승인 후에만 실행됩니다"})
		}
	}
	switch item.Status {
	case "BLOCKED":
		reason := item.BlockedReason
		if reason == "" {
			reason = "보류 상태입니다"
		}
		detail.Blockers = append(detail.Blockers, WorkItemBlocker{Kind: "STATUS", Detail: reason})
	case "DISCARDED":
		detail.Blockers = append(detail.Blockers, WorkItemBlocker{Kind: "STATUS", Detail: "폐기되어 목표 기준선에서 제외되었습니다"})
	case "DONE":
		detail.Blockers = append(detail.Blockers, WorkItemBlocker{Kind: "STATUS", Detail: "검증을 통과해 완료되었습니다"})
	}
	detail.Runs, err = s.RunsForWorkItem(ctx, projectID, workID, 20)
	if err != nil {
		return detail, err
	}
	if commit, commitErr := s.LatestRunCommitForWork(ctx, projectID, workID); commitErr == nil {
		detail.Commit = &commit
	} else if !errors.Is(commitErr, ErrNotFound) {
		return detail, commitErr
	}
	detail.EstimateSource = "manual"
	if item.EstimatedTokens == 0 {
		detail.EstimateSource = "forecast"
	}
	detail.Forecast, err = s.ForecastTokens(ctx, projectID, "CONTINUE_GOAL")
	if err != nil {
		return detail, err
	}
	if detail.EstimateSource == "forecast" && detail.Forecast.Samples == 0 {
		detail.EstimateSource = "none"
	}
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		return detail, err
	}
	detail.Model, err = s.SelectModelForTask(ctx, projectID, project.Model, project.FallbackModel, "CONTINUE_GOAL")
	if err != nil {
		return detail, err
	}
	return detail, nil
}

func (s *Store) RunsForWorkItem(ctx context.Context, projectID, workID string, limit int) ([]RunView, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,COALESCE(r.work_item_id,''),r.task_type,r.state,r.started_at,COALESCE(r.ended_at,''),COALESCE(SUM(CASE WHEN l.token_type<>'cost_usd' THEN l.amount ELSE 0 END),0),COALESCE(SUM(l.cost),0) FROM runs r LEFT JOIN usage_ledger l ON l.run_id=r.id WHERE r.project_id=? AND r.work_item_id=? GROUP BY r.id ORDER BY r.started_at DESC,r.id DESC LIMIT ?`, projectID, workID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RunView
	for rows.Next() {
		var run RunView
		var started, ended string
		if err = rows.Scan(&run.ID, &run.WorkItemID, &run.TaskType, &run.State, &started, &ended, &run.Tokens, &run.CostUSD); err != nil {
			return nil, err
		}
		run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		run.EndedAt, _ = time.Parse(time.RFC3339Nano, ended)
		result = append(result, run)
	}
	return result, rows.Err()
}

// WorkItemQuery filters the backlog. The dashboard used to show four items per
// column with no way to reach the rest.
type WorkItemQuery struct {
	Statuses []string
	Search   string
	Limit    int
}

func (s *Store) SearchWorkItems(ctx context.Context, goalID string, query WorkItemQuery) ([]model.WorkItem, error) {
	conditions := []string{"goal_id=?"}
	args := []any{goalID}
	if len(query.Statuses) > 0 {
		placeholders := make([]string, 0, len(query.Statuses))
		for _, status := range query.Statuses {
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		conditions = append(conditions, "status IN ("+strings.Join(placeholders, ",")+")")
	}
	if trimmed := strings.TrimSpace(query.Search); trimmed != "" {
		conditions = append(conditions, "(LOWER(title) LIKE ? OR LOWER(id) LIKE ? OR LOWER(change_scope) LIKE ? OR LOWER(objective) LIKE ?)")
		pattern := "%" + strings.ToLower(trimmed) + "%"
		args = append(args, pattern, pattern, pattern, pattern)
	}
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id,goal_id,COALESCE(milestone_id,''),type,title,priority,status,risk,change_scope,weight,estimated_tokens,objective,acceptance,blocked_reason FROM work_items WHERE `+
		strings.Join(conditions, " AND ")+` ORDER BY CASE status WHEN 'IN_PROGRESS' THEN 0 WHEN 'APPROVED' THEN 1 WHEN 'BACKLOG' THEN 2 ELSE 3 END, priority DESC, id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.WorkItem
	for rows.Next() {
		var w model.WorkItem
		if err = rows.Scan(&w.ID, &w.GoalID, &w.MilestoneID, &w.Type, &w.Title, &w.Priority, &w.Status, &w.Risk, &w.ChangeScope, &w.Weight, &w.EstimatedTokens, &w.Objective, &w.Acceptance, &w.BlockedReason); err != nil {
			return nil, err
		}
		result = append(result, w)
	}
	return result, rows.Err()
}

// WorkItemPlan is the editable part of a work item's specification.
type WorkItemPlan struct {
	Title, Objective, Acceptance, ChangeScope, Risk string
	Dependencies                                    []string
	Priority, Weight                                *float64
	EstimatedTokens                                 *int64
}

// UpdateWorkItemPlan edits the specification without touching status, so
// planning edits can never move an item through the lifecycle by accident.
// Dependencies are validated to exist, to not point at the item itself, and to
// not close a cycle.
func (s *Store) UpdateWorkItemPlan(ctx context.Context, goalID, workID string, plan WorkItemPlan) (model.WorkItem, error) {
	var updated model.WorkItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return updated, err
	}
	defer tx.Rollback()
	var exists string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM work_items WHERE id=? AND goal_id=?`, workID, goalID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return updated, ErrNotFound
	} else if err != nil {
		return updated, err
	}
	if err = s.setDependencies(ctx, tx, goalID, workID, plan.Dependencies); err != nil {
		return updated, err
	}
	assignments := []string{"objective=?", "acceptance=?", "change_scope=?"}
	args := []any{plan.Objective, plan.Acceptance, plan.ChangeScope}
	if strings.TrimSpace(plan.Title) != "" {
		assignments = append(assignments, "title=?")
		args = append(args, plan.Title)
	}
	if plan.Risk != "" {
		assignments = append(assignments, "risk=?")
		args = append(args, plan.Risk)
	}
	if plan.Priority != nil {
		assignments = append(assignments, "priority=?")
		args = append(args, *plan.Priority)
	}
	if plan.Weight != nil {
		if *plan.Weight <= 0 {
			return updated, errors.New("weight must be positive")
		}
		assignments = append(assignments, "weight=?")
		args = append(args, *plan.Weight)
	}
	if plan.EstimatedTokens != nil {
		if *plan.EstimatedTokens < 0 {
			return updated, errors.New("estimated tokens cannot be negative")
		}
		assignments = append(assignments, "estimated_tokens=?")
		args = append(args, *plan.EstimatedTokens)
	}
	args = append(args, workID, goalID)
	if _, err = tx.ExecContext(ctx, `UPDATE work_items SET `+strings.Join(assignments, ",")+` WHERE id=? AND goal_id=?`, args...); err != nil {
		return updated, err
	}
	if err = tx.Commit(); err != nil {
		return updated, err
	}
	return s.WorkItemByID(ctx, goalID, workID)
}
