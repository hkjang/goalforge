package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Work item owners.
const (
	OwnerAI    = "AI"
	OwnerHuman = "HUMAN"
)

// Takeover records a person taking a work item away from automation. A pause
// button is not enough on its own: the run has to actually be stopped, the
// workspace has to change hands, and what the person then did has to be
// recorded as the new baseline rather than appearing as unexplained drift.
type Takeover struct {
	ID, ProjectID, WorkItemID string
	Reason, Workspace         string
	StoppedRunID              string
	TakenAt, ReturnedAt       time.Time
	ReturnSummary             string
}

// TakeOverWorkItem transfers a work item to a person. It refuses while a run
// is still executing: handing over a workspace that a provider session is
// still writing to produces a conflict neither side can explain.
func (s *Store) TakeOverWorkItem(ctx context.Context, projectID, goalID, workID, reason, workspace string) (Takeover, error) {
	takeover := Takeover{ID: NewID("TKO"), ProjectID: projectID, WorkItemID: workID, Reason: reason,
		Workspace: workspace, TakenAt: time.Now().UTC()}
	var running string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM runs WHERE project_id=? AND state='RUNNING' ORDER BY started_at DESC LIMIT 1`, projectID).Scan(&running)
	if err == nil {
		return takeover, fmt.Errorf("run %s is still executing; stop it first (goalforge cancel) and take over once it has ended", running)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return takeover, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return takeover, err
	}
	defer tx.Rollback()
	var status, owner string
	if err = tx.QueryRowContext(ctx, `SELECT status,COALESCE(owner,'AI') FROM work_items WHERE id=? AND goal_id=?`, workID, goalID).Scan(&status, &owner); errors.Is(err, sql.ErrNoRows) {
		return takeover, ErrNotFound
	} else if err != nil {
		return takeover, err
	}
	if owner == OwnerHuman {
		return takeover, errors.New("work item is already owned by a person")
	}
	if status == "DONE" || status == "DISCARDED" {
		return takeover, fmt.Errorf("work item is %s; there is nothing to take over", status)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_items SET owner=?,status='IN_PROGRESS' WHERE id=? AND goal_id=?`, OwnerHuman, workID, goalID); err != nil {
		return takeover, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO takeovers(id,project_id,work_item_id,reason,workspace,stopped_run_id,taken_at) VALUES(?,?,?,?,?,'',?)`,
		takeover.ID, projectID, workID, reason, workspace, takeover.TakenAt.Format(time.RFC3339Nano)); err != nil {
		return takeover, err
	}
	return takeover, tx.Commit()
}

// ReturnWorkItem hands a work item back to automation and records what the
// person changed as the new baseline.
func (s *Store) ReturnWorkItem(ctx context.Context, projectID, goalID, workID, summary string) (Takeover, error) {
	var takeover Takeover
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return takeover, err
	}
	defer tx.Rollback()
	var taken string
	err = tx.QueryRowContext(ctx, `SELECT id,reason,workspace,taken_at FROM takeovers WHERE project_id=? AND work_item_id=? AND returned_at='' ORDER BY taken_at DESC LIMIT 1`, projectID, workID).
		Scan(&takeover.ID, &takeover.Reason, &takeover.Workspace, &taken)
	if errors.Is(err, sql.ErrNoRows) {
		return takeover, ErrNotFound
	}
	if err != nil {
		return takeover, err
	}
	takeover.ProjectID, takeover.WorkItemID = projectID, workID
	takeover.TakenAt, _ = time.Parse(time.RFC3339Nano, taken)
	takeover.ReturnedAt = time.Now().UTC()
	takeover.ReturnSummary = summary
	if _, err = tx.ExecContext(ctx, `UPDATE takeovers SET returned_at=?,return_summary=? WHERE id=?`,
		takeover.ReturnedAt.Format(time.RFC3339Nano), summary, takeover.ID); err != nil {
		return takeover, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_items SET owner=?,status='BACKLOG' WHERE id=? AND goal_id=?`, OwnerAI, workID, goalID); err != nil {
		return takeover, err
	}
	return takeover, tx.Commit()
}

// ActiveTakeover returns the open takeover for a work item, if any.
func (s *Store) ActiveTakeover(ctx context.Context, projectID, workID string) (Takeover, error) {
	var takeover Takeover
	var taken string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,work_item_id,reason,workspace,taken_at FROM takeovers WHERE project_id=? AND work_item_id=? AND returned_at='' ORDER BY taken_at DESC LIMIT 1`, projectID, workID).
		Scan(&takeover.ID, &takeover.ProjectID, &takeover.WorkItemID, &takeover.Reason, &takeover.Workspace, &taken)
	if errors.Is(err, sql.ErrNoRows) {
		return takeover, ErrNotFound
	}
	if err == nil {
		takeover.TakenAt, _ = time.Parse(time.RFC3339Nano, taken)
	}
	return takeover, err
}
