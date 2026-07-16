package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type ControlRequest struct {
	ID, ProjectID, RunID, Action, Status string
	RequestedAt, HandledAt               time.Time
}

var ErrNoRunningExecution = errors.New("project has no running AI execution")

func (s *Store) RequestRunControl(ctx context.Context, projectID, action string) (ControlRequest, error) {
	var request ControlRequest
	if action != "PAUSE" && action != "CANCEL" {
		return request, errors.New("control action must be PAUSE or CANCEL")
	}
	err := s.db.QueryRowContext(ctx, `SELECT id FROM runs WHERE project_id=? AND state='RUNNING' ORDER BY started_at DESC LIMIT 1`, projectID).Scan(&request.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return request, ErrNoRunningExecution
	}
	if err != nil {
		return request, err
	}
	request.ID, request.ProjectID, request.Action, request.Status = NewID("CTRL"), projectID, action, "PENDING"
	request.RequestedAt = time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO run_control_requests(id,project_id,run_id,action,status,requested_at) VALUES(?,?,?,?,?,?)`, request.ID, projectID, request.RunID, action, request.Status, request.RequestedAt.Format(time.RFC3339Nano))
	if err != nil {
		return request, err
	}
	var requested string
	err = s.db.QueryRowContext(ctx, `SELECT id,requested_at FROM run_control_requests WHERE run_id=? AND action=? AND status='PENDING'`, request.RunID, action).Scan(&request.ID, &requested)
	if err != nil {
		return request, err
	}
	request.RequestedAt, _ = time.Parse(time.RFC3339Nano, requested)
	return request, nil
}

func (s *Store) PendingRunControl(ctx context.Context, runID string) (ControlRequest, error) {
	var request ControlRequest
	var requested string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,run_id,action,status,requested_at FROM run_control_requests WHERE run_id=? AND status='PENDING' ORDER BY requested_at LIMIT 1`, runID).Scan(&request.ID, &request.ProjectID, &request.RunID, &request.Action, &request.Status, &requested)
	if errors.Is(err, sql.ErrNoRows) {
		return request, ErrNotFound
	}
	request.RequestedAt, _ = time.Parse(time.RFC3339Nano, requested)
	return request, err
}

func (s *Store) CompleteRunControl(ctx context.Context, id, status string) error {
	if status != "HANDLED" && status != "FAILED" {
		return errors.New("control status must be HANDLED or FAILED")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE run_control_requests SET status=?,handled_at=? WHERE id=? AND status='PENDING'`, status, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("control request is not pending")
	}
	return nil
}
