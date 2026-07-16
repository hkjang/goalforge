package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/audit"
)

const (
	ApprovalProtectedFiles = "MODIFY_PROTECTED_FILES"
	// ApprovalPublishBranch gates pushing a verified work branch to a remote
	// (SEC-011: external transfers require explicit user approval).
	ApprovalPublishBranch = "PUBLISH_BRANCH"
	// ApprovalMergeBranch gates merging a verified work branch into the
	// protected default branch.
	ApprovalMergeBranch = "MERGE_BRANCH"
)

type Approval struct {
	ID, ProjectID, ActionType, Reason, Status, ConsumedRunID string
	RequestedAt, ApprovedAt                                  time.Time
}

func (s *Store) RequestApproval(ctx context.Context, projectID, actionType, reason string) (Approval, error) {
	var approval Approval
	if projectID == "" || actionType == "" || reason == "" {
		return approval, errors.New("project, action type, and reason are required")
	}
	approval = Approval{ID: NewID("APR"), ProjectID: projectID, ActionType: actionType, Reason: audit.RedactString(reason), Status: "PENDING", RequestedAt: time.Now().UTC()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO approvals(id,project_id,action_type,reason,status,requested_at) VALUES(?,?,?,?,?,?)`, approval.ID, approval.ProjectID, approval.ActionType, approval.Reason, approval.Status, approval.RequestedAt.Format(time.RFC3339Nano))
	return approval, err
}

func (s *Store) Approve(ctx context.Context, projectID, approvalID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE approvals SET status='APPROVED',approved_at=? WHERE id=? AND project_id=? AND status='PENDING'`, time.Now().UTC().Format(time.RFC3339Nano), approvalID, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("approval is not pending")
	}
	return nil
}

func (s *Store) ConsumeApproval(ctx context.Context, projectID, actionType, runID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM approvals WHERE project_id=? AND action_type=? AND status='APPROVED' ORDER BY approved_at,id LIMIT 1`, projectID, actionType).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE approvals SET status='CONSUMED',consumed_run_id=? WHERE id=? AND status='APPROVED'`, runID, id)
	if err != nil {
		return false, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return false, errors.New("approval claim lost")
	}
	return true, tx.Commit()
}

func (s *Store) RecordPolicyViolation(ctx context.Context, projectID, runID, policyType, details string) error {
	if projectID == "" || runID == "" || policyType == "" {
		return errors.New("project, run, and policy type are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO policy_violations(id,project_id,run_id,policy_type,details,created_at) VALUES(?,?,?,?,?,?)`, NewID("POL"), projectID, runID, policyType, audit.RedactString(details), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET state='FAILED' WHERE id=? AND state='VERIFYING'`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE id=(SELECT work_item_id FROM runs WHERE id=?) AND status='VERIFYING'`, runID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE projects SET state='BLOCKED' WHERE id=? AND state='VERIFYING'`, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("project is not awaiting verification")
	}
	return tx.Commit()
}
