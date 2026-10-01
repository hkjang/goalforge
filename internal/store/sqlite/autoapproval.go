package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AutoApprovalRecord is one approval automation made for itself.
//
// It records what allowed it, because an approval nobody can trace back to a
// rule is indistinguishable from one nobody made. When the operator later asks
// why a change was attempted, "the policy permitted it" is only an answer if
// the policy that permitted it is written down beside the decision.
type AutoApprovalRecord struct {
	WorkItemID, ProjectID, StandardID string
	// Basis is the envelope that admitted this item — the criterion, the
	// scope, and the gate that will be able to judge the result.
	Basis      string
	ApprovedAt time.Time
	// Settled is false while the attempt is outstanding. Outcome carries why
	// it ended, which is both the record and the resume point.
	Settled bool
	Passed  bool
	Outcome string
}

// RecordAutoApproval stores an automatic approval.
func (s *Store) RecordAutoApproval(ctx context.Context, record AutoApprovalRecord) error {
	if record.WorkItemID == "" || record.ProjectID == "" {
		return errors.New("work item and project are required")
	}
	if record.ApprovedAt.IsZero() {
		record.ApprovedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO auto_approvals(work_item_id,project_id,standard_id,basis,approved_at,settled,passed,outcome)
VALUES(?,?,?,?,?,0,0,'')
ON CONFLICT(work_item_id) DO UPDATE SET basis=excluded.basis,approved_at=excluded.approved_at`,
		record.WorkItemID, record.ProjectID, record.StandardID, record.Basis,
		record.ApprovedAt.Format(time.RFC3339Nano))
	return err
}

// SettleAutoApproval closes out an automatic attempt.
//
// This is the step that was missing. The autonomy loop refuses to approve an
// item whose previous automatic attempt was settled and failed — "whatever
// stopped it is still there, and a second identical attempt spends budget to
// reach the same place" — and nothing ever settled one. Every record stayed
// outstanding, that guard never fired, and the loop was free to re-approve a
// failing item every sweep.
//
// The detail is kept whether it passed or failed. A failure with no reason
// leaves the next reader — human or automated — to work out from scratch what
// already went wrong once.
func (s *Store) SettleAutoApproval(ctx context.Context, workItemID string, passed bool, detail string) error {
	if workItemID == "" {
		return errors.New("work item is required")
	}
	value := 0
	if passed {
		value = 1
	}
	result, err := s.db.ExecContext(ctx, `UPDATE auto_approvals SET settled=1,passed=?,outcome=? WHERE work_item_id=?`,
		value, detail, workItemID)
	if err != nil {
		return err
	}
	// Refused rather than silently updating nothing. A settle that matched no
	// row would look like it worked, and the guard it feeds would stay blind
	// for exactly the item it was told about.
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s 에 대한 자동 시도 기록이 없습니다", ErrNotFound, workItemID)
	}
	return nil
}

// AutoApprovals lists what automation approved for a project, newest first.
func (s *Store) AutoApprovals(ctx context.Context, projectID string) ([]AutoApprovalRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT work_item_id,project_id,standard_id,basis,approved_at,settled,passed,outcome
FROM auto_approvals WHERE project_id=? ORDER BY approved_at DESC,work_item_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []AutoApprovalRecord
	for rows.Next() {
		var record AutoApprovalRecord
		var approvedAt string
		var settled, passed int
		if err = rows.Scan(&record.WorkItemID, &record.ProjectID, &record.StandardID, &record.Basis,
			&approvedAt, &settled, &passed, &record.Outcome); err != nil {
			return nil, err
		}
		record.ApprovedAt, _ = time.Parse(time.RFC3339Nano, approvedAt)
		record.Settled, record.Passed = settled == 1, passed == 1
		records = append(records, record)
	}
	return records, rows.Err()
}

// AutoApprovalFor returns the record for one work item.
func (s *Store) AutoApprovalFor(ctx context.Context, workItemID string) (AutoApprovalRecord, error) {
	var record AutoApprovalRecord
	var approvedAt string
	var settled, passed int
	err := s.db.QueryRowContext(ctx, `SELECT work_item_id,project_id,standard_id,basis,approved_at,settled,passed,outcome
FROM auto_approvals WHERE work_item_id=?`, workItemID).
		Scan(&record.WorkItemID, &record.ProjectID, &record.StandardID, &record.Basis, &approvedAt,
			&settled, &passed, &record.Outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	if err != nil {
		return record, err
	}
	record.ApprovedAt, _ = time.Parse(time.RFC3339Nano, approvedAt)
	record.Settled, record.Passed = settled == 1, passed == 1
	return record, nil
}

// AutoApprovalsSince counts approvals made in a window, which is what the
// day's allowance is spent against.
//
// Every approval counts, including the ones whose work then failed. An
// allowance that only counted successes would let a loop that fails every time
// run without limit — the loop the allowance exists to stop.
func (s *Store) AutoApprovalsSince(ctx context.Context, projectID string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auto_approvals WHERE project_id=? AND approved_at>=?`,
		projectID, since.UTC().Format(time.RFC3339Nano)).Scan(&count)
	return count, err
}

// SuppliedFindings maps work item IDs to the criterion they were filed for,
// which is how automation tells its own findings from work a person wrote.
func (s *Store) SuppliedFindings(ctx context.Context, projectID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT work_item_id,standard_id FROM standard_findings WHERE project_id=? AND work_item_id<>''`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	findings := map[string]string{}
	for rows.Next() {
		var workItemID, standardID string
		if err = rows.Scan(&workItemID, &standardID); err != nil {
			return nil, err
		}
		findings[workItemID] = standardID
	}
	return findings, rows.Err()
}
