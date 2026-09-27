package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// ApprovalScope binds an approval to the exact change it was granted for.
// Without it an approval was only (project, action type), so an approval
// granted after reviewing one work item's commit could be spent by a later
// run publishing or merging something else entirely.
type ApprovalScope struct {
	WorkItemID string
	// SourceBranch is the verified work branch the change comes from.
	SourceBranch string
	// TargetRef is where the change is applied: the default branch for a
	// merge, the git remote for a publish.
	TargetRef    string
	CommitSHA    string
	FilesChanged int
}

// Scoped reports whether the approval names a specific commit. Approvals
// recorded before scoping existed are unscoped and are not accepted for
// actions that now require a scope.
func (a ApprovalScope) Scoped() bool {
	return a.WorkItemID != "" && a.CommitSHA != ""
}

type Approval struct {
	ID, ProjectID, ActionType, Reason, Status, ConsumedRunID string
	RequestedAt, ApprovedAt                                  time.Time
	Scope                                                    ApprovalScope
}

// StaleApprovalError reports that an approval exists for the work item but
// does not cover the change now being attempted: either the commit moved after
// the review, or the destination is not the one that was approved. Both are
// surfaced rather than reported as "unapproved", because the user needs to
// know their review no longer matches what is about to ship.
type StaleApprovalError struct {
	ApprovalID, WorkItemID string
	// Field is "commit" or "target".
	Field               string
	Approved, Requested string
}

func (e *StaleApprovalError) Error() string {
	if e.Field == "target" {
		return fmt.Sprintf("approval %s covers %s applied to %s, not %s; request approval for the destination you intend",
			e.ApprovalID, e.WorkItemID, e.Approved, e.Requested)
	}
	return fmt.Sprintf("approval %s was granted for commit %s but %s now points at %s; review the new change and request approval again",
		e.ApprovalID, shortSHA(e.Approved), e.WorkItemID, shortSHA(e.Requested))
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func (s *Store) RequestApproval(ctx context.Context, projectID, actionType, reason string) (Approval, error) {
	return s.RequestScopedApproval(ctx, projectID, actionType, reason, ApprovalScope{})
}

// RequestScopedApproval records an approval request bound to a specific
// change. Reviewers see what they are approving, and the approval can only be
// spent on that work item and commit.
func (s *Store) RequestScopedApproval(ctx context.Context, projectID, actionType, reason string, scope ApprovalScope) (Approval, error) {
	var approval Approval
	if projectID == "" || actionType == "" || reason == "" {
		return approval, errors.New("project, action type, and reason are required")
	}
	if scopeRequired(actionType) && !scope.Scoped() {
		return approval, fmt.Errorf("%s approvals must name the work item and commit being approved", actionType)
	}
	approval = Approval{ID: NewID("APR"), ProjectID: projectID, ActionType: actionType, Reason: audit.RedactString(reason), Status: "PENDING", RequestedAt: time.Now().UTC(), Scope: scope}
	_, err := s.db.ExecContext(ctx, `INSERT INTO approvals(id,project_id,action_type,reason,status,requested_at,work_item_id,source_branch,target_ref,commit_sha,files_changed) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		approval.ID, approval.ProjectID, approval.ActionType, approval.Reason, approval.Status, approval.RequestedAt.Format(time.RFC3339Nano),
		scope.WorkItemID, scope.SourceBranch, scope.TargetRef, scope.CommitSHA, scope.FilesChanged)
	return approval, err
}

// scopeRequired lists the actions that transfer verified work outward, where
// approving "a merge, some time, for this project" is not good enough.
func scopeRequired(actionType string) bool {
	return actionType == ApprovalMergeBranch || actionType == ApprovalPublishBranch
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

// RejectApproval declines a pending approval; rejected approvals can never
// be consumed by a run.
func (s *Store) RejectApproval(ctx context.Context, projectID, approvalID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE approvals SET status='REJECTED',approved_at=? WHERE id=? AND project_id=? AND status='PENDING'`, time.Now().UTC().Format(time.RFC3339Nano), approvalID, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("approval is not pending")
	}
	return nil
}

// PendingApproval is an approval joined with its project name for the
// cross-project inbox.
type PendingApproval struct {
	Approval
	ProjectName string
}

func (s *Store) ListAllPendingApprovals(ctx context.Context) ([]PendingApproval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.project_id,a.action_type,a.reason,a.status,a.requested_at,a.work_item_id,a.source_branch,a.target_ref,a.commit_sha,a.files_changed,p.name FROM approvals a JOIN projects p ON p.id=a.project_id WHERE a.status='PENDING' ORDER BY a.requested_at,a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PendingApproval
	for rows.Next() {
		var approval PendingApproval
		var requested string
		if err = rows.Scan(&approval.ID, &approval.ProjectID, &approval.ActionType, &approval.Reason, &approval.Status, &requested,
			&approval.Scope.WorkItemID, &approval.Scope.SourceBranch, &approval.Scope.TargetRef, &approval.Scope.CommitSHA, &approval.Scope.FilesChanged, &approval.ProjectName); err != nil {
			return nil, err
		}
		approval.RequestedAt, _ = time.Parse(time.RFC3339Nano, requested)
		result = append(result, approval)
	}
	return result, rows.Err()
}

func (s *Store) ListPendingApprovals(ctx context.Context, projectID string) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,action_type,reason,status,requested_at,work_item_id,source_branch,target_ref,commit_sha,files_changed FROM approvals WHERE project_id=? AND status='PENDING' ORDER BY requested_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Approval
	for rows.Next() {
		var approval Approval
		var requested string
		if err = rows.Scan(&approval.ID, &approval.ProjectID, &approval.ActionType, &approval.Reason, &approval.Status, &requested,
			&approval.Scope.WorkItemID, &approval.Scope.SourceBranch, &approval.Scope.TargetRef, &approval.Scope.CommitSHA, &approval.Scope.FilesChanged); err != nil {
			return nil, err
		}
		approval.RequestedAt, _ = time.Parse(time.RFC3339Nano, requested)
		result = append(result, approval)
	}
	return result, rows.Err()
}

// ConsumeApproval spends an unscoped approval. Only actions that are not tied
// to a specific commit (protected-file edits within a run) may use it; the
// query refuses scoped rows so a merge approval can never be spent here.
func (s *Store) ConsumeApproval(ctx context.Context, projectID, actionType, runID string) (bool, error) {
	if scopeRequired(actionType) {
		return false, fmt.Errorf("%s approvals are scoped; use ConsumeScopedApproval", actionType)
	}
	return s.consume(ctx, runID, `SELECT id FROM approvals WHERE project_id=? AND action_type=? AND status='APPROVED' AND work_item_id='' ORDER BY approved_at,id LIMIT 1`, projectID, actionType)
}

// ConsumeScopedApproval spends an approval only if it was granted for exactly
// this work item, commit, and target. When an approval exists for the work
// item but names a different commit the change moved after review, and that is
// reported as a stale approval instead of being treated as unapproved: the
// user needs to know their review no longer covers what is about to ship.
func (s *Store) ConsumeScopedApproval(ctx context.Context, projectID, actionType, runID string, scope ApprovalScope) (bool, error) {
	if !scope.Scoped() {
		return false, errors.New("work item and commit SHA are required to consume a scoped approval")
	}
	matched, err := s.consume(ctx, runID,
		`SELECT id FROM approvals WHERE project_id=? AND action_type=? AND status='APPROVED' AND work_item_id=? AND commit_sha=? AND (target_ref='' OR target_ref=?) ORDER BY approved_at,id LIMIT 1`,
		projectID, actionType, scope.WorkItemID, scope.CommitSHA, scope.TargetRef)
	if err != nil || matched {
		return matched, err
	}
	var id, approvedSHA, approvedTarget string
	err = s.db.QueryRowContext(ctx, `SELECT id,commit_sha,target_ref FROM approvals WHERE project_id=? AND action_type=? AND status='APPROVED' AND work_item_id=? ORDER BY approved_at,id LIMIT 1`, projectID, actionType, scope.WorkItemID).Scan(&id, &approvedSHA, &approvedTarget)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if approvedSHA != scope.CommitSHA {
		return false, &StaleApprovalError{ApprovalID: id, WorkItemID: scope.WorkItemID, Field: "commit", Approved: approvedSHA, Requested: scope.CommitSHA}
	}
	return false, &StaleApprovalError{ApprovalID: id, WorkItemID: scope.WorkItemID, Field: "target", Approved: approvedTarget, Requested: scope.TargetRef}
}

func (s *Store) consume(ctx context.Context, runID, query string, args ...any) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, query, args...).Scan(&id)
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

// ApprovalByID reads one approval, scope included, so a reviewer can be shown
// what the decision covers before making it.
func (s *Store) ApprovalByID(ctx context.Context, projectID, approvalID string) (Approval, error) {
	var approval Approval
	var requested, approved string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,action_type,reason,status,requested_at,COALESCE(approved_at,''),COALESCE(consumed_run_id,''),work_item_id,source_branch,target_ref,commit_sha,files_changed FROM approvals WHERE id=? AND project_id=?`, approvalID, projectID).
		Scan(&approval.ID, &approval.ProjectID, &approval.ActionType, &approval.Reason, &approval.Status, &requested, &approved, &approval.ConsumedRunID,
			&approval.Scope.WorkItemID, &approval.Scope.SourceBranch, &approval.Scope.TargetRef, &approval.Scope.CommitSHA, &approval.Scope.FilesChanged)
	if errors.Is(err, sql.ErrNoRows) {
		return approval, ErrNotFound
	}
	if err != nil {
		return approval, err
	}
	approval.RequestedAt, _ = time.Parse(time.RFC3339Nano, requested)
	approval.ApprovedAt, _ = time.Parse(time.RFC3339Nano, approved)
	return approval, nil
}
