package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// IntegrationCheck records whether the default branch still needs verifying
// after work was merged into it. Each work item verified in its own worktree;
// nothing has yet verified their combination, and that gap is exactly where a
// green run and a broken main branch coexist.
type IntegrationCheck struct {
	ProjectID, Reason, TargetSHA string
	Pending, LastPassed          bool
	LastSHA, LastDetails         string
	UpdatedAt                    time.Time
}

// MarkIntegrationPending flags the default branch as unverified since a merge.
func (s *Store) MarkIntegrationPending(ctx context.Context, projectID, reason, targetSHA string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO integration_checks(project_id,pending,reason,target_sha,updated_at) VALUES(?,1,?,?,?)
ON CONFLICT(project_id) DO UPDATE SET pending=1,reason=excluded.reason,target_sha=excluded.target_sha,updated_at=excluded.updated_at`,
		projectID, reason, targetSHA, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// RecordIntegrationResult stores the outcome of verifying the merged result.
// A pass clears the pending flag; a failure keeps it, because the branch is
// still not known to work.
func (s *Store) RecordIntegrationResult(ctx context.Context, projectID, sha, details string, passed bool) error {
	pending, passedValue := 1, 0
	if passed {
		pending, passedValue = 0, 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO integration_checks(project_id,pending,reason,last_passed,last_sha,last_details,updated_at) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(project_id) DO UPDATE SET pending=excluded.pending,reason=excluded.reason,last_passed=excluded.last_passed,last_sha=excluded.last_sha,last_details=excluded.last_details,updated_at=excluded.updated_at`,
		projectID, pending, integrationReason(passed), passedValue, sha, details, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func integrationReason(passed bool) string {
	if passed {
		return ""
	}
	return "통합 검증이 실패했습니다. 병합된 결과가 아직 동작하지 않습니다."
}

func (s *Store) IntegrationStatus(ctx context.Context, projectID string) (IntegrationCheck, error) {
	var check IntegrationCheck
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT project_id,pending,reason,target_sha,last_passed,last_sha,last_details,updated_at FROM integration_checks WHERE project_id=?`, projectID).
		Scan(&check.ProjectID, &check.Pending, &check.Reason, &check.TargetSHA, &check.LastPassed, &check.LastSHA, &check.LastDetails, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return IntegrationCheck{ProjectID: projectID}, nil
	}
	if err == nil {
		check.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	}
	return check, err
}
