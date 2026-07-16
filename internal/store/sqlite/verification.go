package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/audit"
	"github.com/goalforge/goalforge/internal/model"
)

type VerificationRecord struct {
	RunID, CheckType, Status, ActualValue, Command, Output string
	ExitCode                                               int
	Duration                                               time.Duration
	Required                                               bool
}

func (s *Store) RecordRunVerification(ctx context.Context, r VerificationRecord) error {
	if r.RunID == "" || r.CheckType == "" {
		return errors.New("run ID and check type are required")
	}
	var goalID string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(w.goal_id,g.id) FROM runs r LEFT JOIN work_items w ON w.id=r.work_item_id LEFT JOIN goals g ON g.project_id=r.project_id AND g.status='ACTIVE' WHERE r.id=? LIMIT 1`, r.RunID).Scan(&goalID)
	if err != nil {
		return err
	}
	required := 0
	if r.Required {
		required = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,run_id,check_type,status,actual_value,command,exit_code,duration_ms,required,output,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, goalID, r.RunID, r.CheckType, r.Status, r.ActualValue, audit.RedactString(r.Command), r.ExitCode, r.Duration.Milliseconds(), required, audit.RedactString(r.Output), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ApplyVerificationOutcome(ctx context.Context, runID string, passed bool) (model.Goal, error) {
	var goal model.Goal
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return goal, err
	}
	defer tx.Rollback()
	var projectID, workID, goalID string
	err = tx.QueryRowContext(ctx, `SELECT r.project_id,COALESCE(r.work_item_id,''),COALESCE(w.goal_id,g.id) FROM runs r LEFT JOIN work_items w ON w.id=r.work_item_id LEFT JOIN goals g ON g.project_id=r.project_id AND g.status='ACTIVE' WHERE r.id=? AND r.state='VERIFYING'`, runID).Scan(&projectID, &workID, &goalID)
	if errors.Is(err, sql.ErrNoRows) {
		return goal, errors.New("run is not awaiting verification")
	}
	if err != nil {
		return goal, err
	}
	if passed {
		if workID != "" {
			if _, err = tx.ExecContext(ctx, `UPDATE work_items SET status='DONE' WHERE id=? AND status='VERIFYING'`, workID); err != nil {
				return goal, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET state='CHECKPOINTING' WHERE id=?`, runID); err != nil {
			return goal, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE projects SET state='CHECKPOINTING' WHERE id=? AND state='VERIFYING'`, projectID); err != nil {
			return goal, err
		}
	} else {
		if workID != "" {
			if _, err = tx.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE id=? AND status='VERIFYING'`, workID); err != nil {
				return goal, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET state='REPAIR_REQUIRED' WHERE id=?`, runID); err != nil {
			return goal, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE projects SET state='REPAIR_REQUIRED' WHERE id=? AND state='VERIFYING'`, projectID); err != nil {
			return goal, err
		}
	}
	var created string
	err = tx.QueryRowContext(ctx, `SELECT id,project_id,version,title,objective,status,change_reason,created_at FROM goals WHERE id=?`, goalID).Scan(&goal.ID, &goal.ProjectID, &goal.Version, &goal.Title, &goal.Objective, &goal.Status, &goal.ChangeReason, &created)
	if err != nil {
		return goal, err
	}
	goal.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	rows, err := tx.QueryContext(ctx, `SELECT criterion_type,expected_value FROM goal_criteria WHERE goal_id=?`, goal.ID)
	if err != nil {
		return goal, err
	}
	for rows.Next() {
		var c model.Criterion
		if err = rows.Scan(&c.Type, &c.ExpectedValue); err != nil {
			rows.Close()
			return goal, err
		}
		goal.Criteria = append(goal.Criteria, c)
	}
	if err = rows.Close(); err != nil {
		return goal, err
	}
	return goal, tx.Commit()
}

func (s *Store) FinalizeCheckpoint(ctx context.Context, projectID, goalID string, complete bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state := "READY"
	if complete {
		state = "COMPLETED"
		if _, err = tx.ExecContext(ctx, `UPDATE goals SET status='COMPLETED' WHERE id=? AND status='ACTIVE'`, goalID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE projects SET state=? WHERE id=? AND state='CHECKPOINTING'`, state, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("project is not checkpointing")
	}
	return tx.Commit()
}
