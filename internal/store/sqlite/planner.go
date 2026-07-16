package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func (s *Store) CreateScoredIdea(ctx context.Context, w model.WorkItem, score model.IdeaScore) (model.WorkItem, error) {
	if score.Fingerprint == "" {
		return w, errors.New("idea fingerprint is required")
	}
	if w.ID == "" {
		w.ID = NewID("IDEA")
	}
	if w.Type == "" {
		w.Type = "IDEA"
	}
	if w.Weight <= 0 {
		w.Weight = 1
	}
	if w.Risk == "" {
		w.Risk = "medium"
	}
	w.Priority = score.PriorityScore
	if score.ApprovalRequired {
		w.Status = "BLOCKED"
	} else if w.Status == "" {
		w.Status = "BACKLOG"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return w, err
	}
	defer tx.Rollback()
	var milestone any
	if w.MilestoneID != "" {
		milestone = w.MilestoneID
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_items(id,goal_id,milestone_id,type,title,priority,status,dependency,risk,change_scope,weight,estimated_tokens) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, w.ID, w.GoalID, milestone, w.Type, w.Title, w.Priority, w.Status, w.Dependency, w.Risk, w.ChangeScope, w.Weight, w.EstimatedTokens); err != nil {
		return w, err
	}
	scope, approval := 0, 0
	if score.ScopeExpansion {
		scope = 1
	}
	if score.ApprovalRequired {
		approval = 1
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO idea_scores(work_item_id,goal_contribution,user_value,operational_need,feasibility,risk_reduction,difficulty,priority_score,expected_change_scope,fingerprint,scope_expansion,approval_required) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, w.ID, score.GoalContribution, score.UserValue, score.OperationalNeed, score.Feasibility, score.RiskReduction, score.Difficulty, score.PriorityScore, score.ExpectedChangeScope, score.Fingerprint, scope, approval); err != nil {
		return w, err
	}
	return w, tx.Commit()
}

func (s *Store) RecordLoopSignal(ctx context.Context, projectID, workItemID, signalType, fingerprint, runID string) (int, error) {
	if projectID == "" || signalType == "" || fingerprint == "" {
		return 0, errors.New("project, signal type, and fingerprint are required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO loop_signals(project_id,work_item_id,signal_type,fingerprint,occurrences,last_run_id,updated_at) VALUES(?,?,?,?,1,?,?) ON CONFLICT(project_id,work_item_id,signal_type,fingerprint) DO UPDATE SET occurrences=occurrences+1,last_run_id=excluded.last_run_id,updated_at=excluded.updated_at`, projectID, workItemID, signalType, fingerprint, runID, now)
	if err != nil {
		return 0, err
	}
	var count int
	err = s.db.QueryRowContext(ctx, `SELECT occurrences FROM loop_signals WHERE project_id=? AND work_item_id=? AND signal_type=? AND fingerprint=?`, projectID, workItemID, signalType, fingerprint).Scan(&count)
	return count, err
}

func (s *Store) BlockProjectForLoop(ctx context.Context, projectID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET state='BLOCKED' WHERE id=? AND state NOT IN ('COMPLETED','CANCELLED','BLOCKED')`, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 1 {
		return nil
	}
	var state string
	if err = s.db.QueryRowContext(ctx, `SELECT state FROM projects WHERE id=?`, projectID).Scan(&state); err != nil {
		return err
	}
	if state == "BLOCKED" {
		return nil
	}
	return errors.New("terminal project cannot be blocked by loop policy")
}

func (s *Store) CountUnimplemented(ctx context.Context, goalID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE goal_id=? AND status IN ('BACKLOG','APPROVED','IN_PROGRESS','VERIFYING')`, goalID).Scan(&count)
	return count, err
}

func (s *Store) ClaimNextWorkItem(ctx context.Context, goalID string) (model.WorkItem, error) {
	var w model.WorkItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return w, err
	}
	defer tx.Rollback()
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE goal_id=? AND status='IN_PROGRESS'`, goalID).Scan(&active); err != nil {
		return w, err
	}
	if active > 0 {
		return w, errors.New("implementation WIP limit reached")
	}
	err = tx.QueryRowContext(ctx, `SELECT w.id,w.goal_id,COALESCE(w.milestone_id,''),w.type,w.title,w.priority,w.status,w.dependency,w.risk,w.change_scope,w.weight,w.estimated_tokens FROM work_items w LEFT JOIN idea_scores i ON i.work_item_id=w.id WHERE w.goal_id=? AND w.status IN ('APPROVED','BACKLOG') AND COALESCE(i.approval_required,0)=0 AND (w.dependency='' OR EXISTS(SELECT 1 FROM work_items d WHERE d.id=w.dependency AND d.status='DONE')) ORDER BY CASE w.status WHEN 'APPROVED' THEN 0 ELSE 1 END,w.priority DESC,w.id LIMIT 1`, goalID).Scan(&w.ID, &w.GoalID, &w.MilestoneID, &w.Type, &w.Title, &w.Priority, &w.Status, &w.Dependency, &w.Risk, &w.ChangeScope, &w.Weight, &w.EstimatedTokens)
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET status='IN_PROGRESS' WHERE id=? AND status=?`, w.ID, w.Status)
	if err != nil {
		return w, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return w, errors.New("work item claim lost")
	}
	w.Status = "IN_PROGRESS"
	return w, tx.Commit()
}
