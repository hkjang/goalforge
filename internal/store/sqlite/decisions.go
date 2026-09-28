package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DesignDecision records why a structure was chosen and what was ruled out.
// Without it every new session re-derives the same architecture, and nothing
// stops one of them quietly reversing a choice that was already made.
type DesignDecision struct {
	ID, ProjectID, GoalID          string
	Title, Context, Decision       string
	Alternatives, Consequences     string
	Status, SupersededBy, WorkItem string
	// BaseCommit is what the repository looked like when the decision was
	// made, and Scope names the files it was about. Together they are what
	// lets a later reader be told the decision may no longer hold, instead of
	// being handed it as current fact forever.
	BaseCommit string
	Scope      string
	CreatedAt  time.Time
}

// Decision statuses.
const (
	DecisionAccepted   = "ACCEPTED"
	DecisionSuperseded = "SUPERSEDED"
)

func (s *Store) RecordDecision(ctx context.Context, decision DesignDecision) (DesignDecision, error) {
	if decision.ProjectID == "" || strings.TrimSpace(decision.Title) == "" || strings.TrimSpace(decision.Decision) == "" {
		return decision, errors.New("project, title, and decision are required")
	}
	if decision.ID == "" {
		decision.ID = NewID("DEC")
	}
	if decision.Status == "" {
		decision.Status = DecisionAccepted
	}
	if decision.CreatedAt.IsZero() {
		decision.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO design_decisions(id,project_id,goal_id,work_item_id,title,context,decision,alternatives,consequences,status,superseded_by,base_commit,scope,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,'',?,?,?)`,
		decision.ID, decision.ProjectID, decision.GoalID, decision.WorkItem, decision.Title, decision.Context, decision.Decision,
		decision.Alternatives, decision.Consequences, decision.Status, decision.BaseCommit, decision.Scope, decision.CreatedAt.Format(time.RFC3339Nano))
	return decision, err
}

// SupersedeDecision marks an earlier decision as replaced. Decisions are never
// deleted: the record of what was tried and rejected is the point.
func (s *Store) SupersedeDecision(ctx context.Context, projectID, decisionID, replacementID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE design_decisions SET status=?,superseded_by=? WHERE id=? AND project_id=? AND status=?`,
		DecisionSuperseded, replacementID, decisionID, projectID, DecisionAccepted)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

// ListDecisions returns a project's decisions, accepted ones first.
func (s *Store) ListDecisions(ctx context.Context, projectID string, includeSuperseded bool) ([]DesignDecision, error) {
	query := `SELECT id,project_id,COALESCE(goal_id,''),COALESCE(work_item_id,''),title,context,decision,alternatives,consequences,status,COALESCE(superseded_by,''),COALESCE(base_commit,''),COALESCE(scope,''),created_at FROM design_decisions WHERE project_id=?`
	if !includeSuperseded {
		query += ` AND status='` + DecisionAccepted + `'`
	}
	query += ` ORDER BY created_at DESC,id DESC`
	rows, err := s.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DesignDecision
	for rows.Next() {
		var decision DesignDecision
		var created string
		if err = rows.Scan(&decision.ID, &decision.ProjectID, &decision.GoalID, &decision.WorkItem, &decision.Title, &decision.Context,
			&decision.Decision, &decision.Alternatives, &decision.Consequences, &decision.Status, &decision.SupersededBy, &decision.BaseCommit, &decision.Scope, &created); err != nil {
			return nil, err
		}
		decision.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, decision)
	}
	return result, rows.Err()
}

func (s *Store) DecisionByID(ctx context.Context, projectID, decisionID string) (DesignDecision, error) {
	var decision DesignDecision
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,COALESCE(goal_id,''),COALESCE(work_item_id,''),title,context,decision,alternatives,consequences,status,COALESCE(superseded_by,''),COALESCE(base_commit,''),COALESCE(scope,''),created_at FROM design_decisions WHERE id=? AND project_id=?`, decisionID, projectID).
		Scan(&decision.ID, &decision.ProjectID, &decision.GoalID, &decision.WorkItem, &decision.Title, &decision.Context,
			&decision.Decision, &decision.Alternatives, &decision.Consequences, &decision.Status, &decision.SupersededBy, &decision.BaseCommit, &decision.Scope, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return decision, ErrNotFound
	}
	if err == nil {
		decision.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	}
	return decision, err
}
