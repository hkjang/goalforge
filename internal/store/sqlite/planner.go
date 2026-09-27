package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/notify"
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
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_items(id,goal_id,milestone_id,type,title,priority,status,risk,change_scope,weight,estimated_tokens,objective,acceptance) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, w.ID, w.GoalID, milestone, w.Type, w.Title, w.Priority, w.Status, w.Risk, w.ChangeScope, w.Weight, w.EstimatedTokens, w.Objective, w.Acceptance); err != nil {
		return w, err
	}
	if err = s.setDependencies(ctx, tx, w.GoalID, w.ID, w.Dependencies); err != nil {
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

// IdeaScoresForGoal returns the persisted scoring breakdown per work item so
// triage surfaces can rank and explain candidates.
func (s *Store) IdeaScoresForGoal(ctx context.Context, goalID string) (map[string]model.IdeaScore, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.work_item_id,i.goal_contribution,i.user_value,i.operational_need,i.feasibility,i.risk_reduction,i.difficulty,i.priority_score,i.expected_change_scope,i.fingerprint,i.scope_expansion,i.approval_required FROM idea_scores i JOIN work_items w ON w.id=i.work_item_id WHERE w.goal_id=?`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]model.IdeaScore)
	for rows.Next() {
		var id string
		var score model.IdeaScore
		if err = rows.Scan(&id, &score.GoalContribution, &score.UserValue, &score.OperationalNeed, &score.Feasibility, &score.RiskReduction, &score.Difficulty, &score.PriorityScore, &score.ExpectedChangeScope, &score.Fingerprint, &score.ScopeExpansion, &score.ApprovalRequired); err != nil {
			return nil, err
		}
		result[id] = score
	}
	return result, rows.Err()
}

func (s *Store) BlockProjectForLoop(ctx context.Context, projectID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET state='BLOCKED' WHERE id=? AND state NOT IN ('COMPLETED','CANCELLED','BLOCKED')`, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 1 {
		_ = notify.Post(ctx, notify.Event{Project: projectID, Name: s.projectName(ctx, projectID), State: "BLOCKED", Reason: "repeated loop signals require user review"})
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

// ErrAllCandidatesConflict means there is executable work, but every candidate
// overlaps something already being implemented. It is distinct from ErrNotFound
// ("there is no work") because it clears on its own once a run finishes, so a
// worker should wait rather than hand control back.
var ErrAllCandidatesConflict = errors.New("all claimable work overlaps items already in progress")

// claimCandidateLimit bounds how far down the priority order a claim will look
// for work that does not collide with what is already running.
const claimCandidateLimit = 50

// ClaimNextWorkItem takes the highest-priority item that can actually run now.
// Candidates whose declared change scope overlaps work already in progress are
// skipped rather than refused: stopping at the first conflict made a raised WIP
// limit inert, because one busy area of the tree blocked the whole backlog.
func (s *Store) ClaimNextWorkItem(ctx context.Context, goalID string) (model.WorkItem, error) {
	var w model.WorkItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return w, err
	}
	defer tx.Rollback()
	limit, err := s.wipLimit(ctx, tx, goalID)
	if err != nil {
		return w, err
	}
	active, err := s.activeWorkItems(ctx, tx, goalID, "")
	if err != nil {
		return w, err
	}
	agentHeld := 0
	for _, item := range active {
		if item.Owner != OwnerHuman {
			agentHeld++
		}
	}
	if agentHeld >= limit {
		return w, fmt.Errorf("implementation WIP limit reached: %d of %d items in progress", agentHeld, limit)
	}
	rows, err := tx.QueryContext(ctx, `SELECT w.id,w.goal_id,COALESCE(w.milestone_id,''),w.type,w.title,w.priority,w.status,w.risk,w.change_scope,w.weight,w.estimated_tokens,w.objective,w.acceptance,w.blocked_reason FROM work_items w LEFT JOIN idea_scores i ON i.work_item_id=w.id WHERE w.goal_id=? AND w.status IN ('APPROVED','BACKLOG') AND COALESCE(w.owner,'AI')='AI' AND COALESCE(i.approval_required,0)=0 AND NOT EXISTS(SELECT 1 FROM work_item_dependencies d LEFT JOIN work_items p ON p.id=d.depends_on_id WHERE d.work_item_id=w.id AND COALESCE(p.status,'')<>'DONE') ORDER BY CASE w.status WHEN 'APPROVED' THEN 0 ELSE 1 END,w.priority DESC,w.id LIMIT ?`, goalID, claimCandidateLimit)
	if err != nil {
		return w, err
	}
	var candidates []model.WorkItem
	for rows.Next() {
		var candidate model.WorkItem
		if err = rows.Scan(&candidate.ID, &candidate.GoalID, &candidate.MilestoneID, &candidate.Type, &candidate.Title, &candidate.Priority, &candidate.Status, &candidate.Risk, &candidate.ChangeScope, &candidate.Weight, &candidate.EstimatedTokens, &candidate.Objective, &candidate.Acceptance, &candidate.BlockedReason); err != nil {
			rows.Close()
			return w, err
		}
		candidates = append(candidates, candidate)
	}
	if err = rows.Close(); err != nil {
		return w, err
	}
	if len(candidates) == 0 {
		return w, ErrNotFound
	}
	chosen := -1
	for i, candidate := range candidates {
		if len(scopeConflicts(candidate.ChangeScope, active)) == 0 {
			chosen = i
			break
		}
	}
	if chosen < 0 {
		return w, ErrAllCandidatesConflict
	}
	w = candidates[chosen]
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET status='IN_PROGRESS' WHERE id=? AND status=?`, w.ID, w.Status)
	if err != nil {
		return w, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return w, errors.New("work item claim lost")
	}
	w.Status = "IN_PROGRESS"
	if w.Dependencies, err = s.dependenciesOf(ctx, tx, w.ID); err != nil {
		return w, err
	}
	return w, tx.Commit()
}
