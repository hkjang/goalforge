package sqlite

import (
	"context"
	"fmt"
	"time"
)

// Evidence staleness reasons.
const (
	StaleCriteriaChanged = "goal criteria changed"
	StaleGateChanged     = "verification gate changed"
	StaleCodeChanged     = "verified code changed"
)

// InvalidateEvidence marks a goal's verification results as needing
// re-verification. A passing result is a statement about a particular tree
// checked by a particular command; when either changes the result stops being
// evidence for the current state, and treating it as current is how a goal
// gets reported complete on a check that no longer applies.
func (s *Store) InvalidateEvidence(ctx context.Context, goalID, reason string, checkTypes ...string) (int64, error) {
	query := `UPDATE verification_results SET stale=1,stale_reason=? WHERE goal_id=? AND stale=0 AND status='PASSED'`
	args := []any{reason, goalID}
	if len(checkTypes) > 0 {
		placeholders := ""
		for i, checkType := range checkTypes {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, checkType)
		}
		query += ` AND check_type IN (` + placeholders + `)`
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// InvalidateProjectEvidence invalidates the active goal's evidence, used when
// the repository itself moves under a project.
func (s *Store) InvalidateProjectEvidence(ctx context.Context, projectID, reason string) (int64, error) {
	goal, err := s.CurrentGoal(ctx, projectID)
	if err != nil {
		return 0, err
	}
	return s.InvalidateEvidence(ctx, goal.ID, reason)
}

// VerificationRelaxation records a change that made passing easier rather than
// making the code better: a lowered threshold, a gate that stopped being
// required, deleted tests. These are legitimate sometimes, which is why they
// are recorded for review rather than blocked.
type VerificationRelaxation struct {
	ID, ProjectID, RunID, Kind, Detail string
	Before, After                      string
	CreatedAt                          time.Time
}

func (s *Store) RecordRelaxation(ctx context.Context, relaxation VerificationRelaxation) error {
	if relaxation.ProjectID == "" || relaxation.Kind == "" {
		return fmt.Errorf("project and kind are required")
	}
	if relaxation.ID == "" {
		relaxation.ID = NewID("RLX")
	}
	if relaxation.CreatedAt.IsZero() {
		relaxation.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO verification_relaxations(id,project_id,run_id,kind,detail,before_value,after_value,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		relaxation.ID, relaxation.ProjectID, relaxation.RunID, relaxation.Kind, relaxation.Detail,
		relaxation.Before, relaxation.After, relaxation.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListRelaxations(ctx context.Context, projectID string, limit int) ([]VerificationRelaxation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,COALESCE(run_id,''),kind,detail,before_value,after_value,created_at FROM verification_relaxations WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []VerificationRelaxation
	for rows.Next() {
		var relaxation VerificationRelaxation
		var created string
		if err = rows.Scan(&relaxation.ID, &relaxation.ProjectID, &relaxation.RunID, &relaxation.Kind, &relaxation.Detail,
			&relaxation.Before, &relaxation.After, &created); err != nil {
			return nil, err
		}
		relaxation.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, relaxation)
	}
	return result, rows.Err()
}

// RecordIntegrationEvidence stores verification results that belong to no
// single run: the gates executed against the merged default branch. They are
// recorded as current evidence so a goal can complete on the integrated
// result, which is the only state that actually ships.
func (s *Store) RecordIntegrationEvidence(ctx context.Context, goalID, commitSHA string, records []VerificationRecord) error {
	return s.recordExternalEvidence(ctx, goalID, "integration@"+shortSHA(commitSHA), records)
}

// RecordHumanEvidence stores gate results from a hand-edited workspace. A
// person's change is not exempt from verification; it just has no provider run
// to attach the evidence to.
func (s *Store) RecordHumanEvidence(ctx context.Context, goalID, workItemID string, records []VerificationRecord) error {
	return s.recordExternalEvidence(ctx, goalID, "human@"+workItemID, records)
}

func (s *Store) recordExternalEvidence(ctx context.Context, goalID, source string, records []VerificationRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, record := range records {
		required := 0
		if record.Required {
			required = 1
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO verification_results(goal_id,run_id,check_type,status,actual_value,command,exit_code,duration_ms,required,output,failure_kind,repair_mode,stale,stale_reason,created_at) VALUES(?,NULL,?,?,?,?,?,?,?,?,?,?,0,'',?)`,
			goalID, record.CheckType, record.Status, record.ActualValue, source,
			record.ExitCode, record.Duration.Milliseconds(), required, record.Output, record.FailureKind, record.RepairMode, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
