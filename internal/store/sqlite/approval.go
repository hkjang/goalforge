package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	// ApprovalRemoveTests gates deleting tests that already existed. Removing
	// an obsolete test can be correct, but it is not a decision a run may make
	// for itself while being judged by what remains.
	ApprovalRemoveTests = "REMOVE_TESTS"
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return approval, err
	}
	defer tx.Rollback()
	requestedAt := chainStamp(approval.RequestedAt)
	if _, err = tx.ExecContext(ctx, `INSERT INTO approvals(id,project_id,action_type,reason,status,requested_at,work_item_id,source_branch,target_ref,commit_sha,files_changed) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		approval.ID, approval.ProjectID, approval.ActionType, approval.Reason, approval.Status, requestedAt,
		scope.WorkItemID, scope.SourceBranch, scope.TargetRef, scope.CommitSHA, scope.FilesChanged); err != nil {
		return approval, err
	}
	// Each transition appends a link, so the chain carries the approval's
	// whole history rather than only the state it started in.
	if err = appendChain(ctx, tx, ChainApproval, approval.ID,
		approvalDigest(approval.ID, projectID, actionType, approval.Status, scope.WorkItemID, scope.CommitSHA, requestedAt), requestedAt); err != nil {
		return approval, err
	}
	return approval, tx.Commit()
}

// scopeRequired lists the actions that transfer verified work outward, where
// approving "a merge, some time, for this project" is not good enough.
func scopeRequired(actionType string) bool {
	return actionType == ApprovalMergeBranch || actionType == ApprovalPublishBranch
}

func (s *Store) Approve(ctx context.Context, projectID, approvalID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE approvals SET status='APPROVED',approved_at=? WHERE id=? AND project_id=? AND status='PENDING'`, chainStamp(time.Now()), approvalID, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("approval is not pending")
	}
	if err = chainApprovalState(ctx, tx, approvalID); err != nil {
		return err
	}
	return tx.Commit()
}

// chainApprovalState appends a link for an approval's current state, read back
// inside the same transaction so the digest describes what was actually
// written rather than what the caller meant to write.
func chainApprovalState(ctx context.Context, tx *sql.Tx, approvalID string) error {
	var id, projectID, actionType, status, workItemID, commitSHA, requestedAt string
	if err := tx.QueryRowContext(ctx, `SELECT id,project_id,action_type,status,COALESCE(work_item_id,''),COALESCE(commit_sha,''),requested_at FROM approvals WHERE id=?`, approvalID).
		Scan(&id, &projectID, &actionType, &status, &workItemID, &commitSHA, &requestedAt); err != nil {
		return err
	}
	return appendChain(ctx, tx, ChainApproval, id,
		approvalDigest(id, projectID, actionType, status, workItemID, commitSHA, requestedAt), chainStamp(time.Now()))
}

// RejectionCategories are the reasons a person turns work down. Recording
// which one applies is what turns individual rejections into a signal about
// where the automation is actually weak.
var RejectionCategories = []string{"code_quality", "misunderstood_requirement", "too_broad", "insufficient_evidence", "not_needed", "other"}

func ValidRejectionCategory(category string) bool {
	for _, candidate := range RejectionCategories {
		if candidate == category {
			return true
		}
	}
	return false
}

// RejectApproval declines a pending approval; rejected approvals can never
// be consumed by a run.
func (s *Store) RejectApproval(ctx context.Context, projectID, approvalID string) error {
	return s.RejectApprovalWithReason(ctx, projectID, approvalID, "", "")
}

// RejectApprovalWithReason records why the work was turned down. Without the
// reason a rejection only stops one change; with it, the pattern of rejections
// says what to fix.
func (s *Store) RejectApprovalWithReason(ctx context.Context, projectID, approvalID, category, note string) error {
	if category != "" && !ValidRejectionCategory(category) {
		return fmt.Errorf("rejection category must be one of %s", strings.Join(RejectionCategories, ", "))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE approvals SET status='REJECTED',approved_at=?,rejection_category=?,rejection_note=? WHERE id=? AND project_id=? AND status='PENDING'`,
		chainStamp(time.Now()), category, audit.RedactString(note), approvalID, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("approval is not pending")
	}
	if err = chainApprovalState(ctx, tx, approvalID); err != nil {
		return err
	}
	return tx.Commit()
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

// scopedApprovalRelease is the condition under which an approval row is about
// the release a caller is holding: this project, this action, this work item,
// this commit, and either this destination or none recorded.
//
// It is a single constant because both sides of an approval's life read it.
// The spending side adds status='APPROVED'; the issuing side asks whether a row
// exists at all. Written out twice they would drift, and either direction of
// drift is a defect: narrower on the issuing side stacks duplicate approvals,
// wider skips issuing a release the spending side then cannot find.
const scopedApprovalRelease = `project_id=? AND action_type=? AND work_item_id=? AND commit_sha=? AND (target_ref='' OR target_ref=?)`

// scopedApprovalArgs binds scopedApprovalRelease, in its order.
func scopedApprovalArgs(projectID, actionType string, scope ApprovalScope) []any {
	return []any{projectID, actionType, scope.WorkItemID, scope.CommitSHA, scope.TargetRef}
}

// ScopedApprovalExists reports whether this exact release has already been put
// to anyone, in any state.
//
// Status is deliberately not filtered. Pending means a request is already
// waiting for a reviewer, approved means one is standing ready to spend,
// consumed means the release already happened, and rejected means a person
// said no to this commit. In every one of those the answer to "should another
// request be filed for it" is no — and for the last two, filing one would
// release the same commit twice or overrule the reviewer.
//
// This is not a judgement about what the autonomy envelope permits; that
// question has one home and this is not a second copy of it. It answers only
// whether the decision has already been taken.
func (s *Store) ScopedApprovalExists(ctx context.Context, projectID, actionType string, scope ApprovalScope) (bool, error) {
	if !scope.Scoped() {
		return false, errors.New("work item and commit SHA are required to look up a scoped approval")
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM approvals WHERE `+scopedApprovalRelease+` LIMIT 1`,
		scopedApprovalArgs(projectID, actionType, scope)...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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
		`SELECT id FROM approvals WHERE status='APPROVED' AND `+scopedApprovalRelease+` ORDER BY approved_at,id LIMIT 1`,
		scopedApprovalArgs(projectID, actionType, scope)...)
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
	if err = chainApprovalState(ctx, tx, id); err != nil {
		return false, err
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

// RejectionStat counts rejections by category so the recurring reason is
// visible rather than buried in individual approvals.
type RejectionStat struct {
	Category string
	Count    int
	Examples []string
}

func (s *Store) RejectionStats(ctx context.Context, projectID string) ([]RejectionStat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(NULLIF(rejection_category,''),'unrecorded'),COUNT(*),COALESCE(GROUP_CONCAT(NULLIF(rejection_note,''),'|'),'') FROM approvals WHERE project_id=? AND status='REJECTED' GROUP BY 1 ORDER BY 2 DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RejectionStat
	for rows.Next() {
		var stat RejectionStat
		var notes string
		if err = rows.Scan(&stat.Category, &stat.Count, &notes); err != nil {
			return nil, err
		}
		for _, note := range strings.Split(notes, "|") {
			if trimmed := strings.TrimSpace(note); trimmed != "" && len(stat.Examples) < 3 {
				stat.Examples = append(stat.Examples, trimmed)
			}
		}
		result = append(result, stat)
	}
	return result, rows.Err()
}
