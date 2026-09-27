package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/model"
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

// IntegrationRepairScope marks the work item created when merged branches fail
// together. It is recognizable so a second failure adds to the existing item
// rather than piling up duplicates nobody triages.
const IntegrationRepairScope = "integration"

// OpenIntegrationRepair returns the outstanding integration-fix work item, if
// one exists.
func (s *Store) OpenIntegrationRepair(ctx context.Context, goalID string) (model.WorkItem, error) {
	var item model.WorkItem
	err := s.db.QueryRowContext(ctx, `SELECT id,goal_id,COALESCE(milestone_id,''),type,title,priority,status,risk,change_scope,weight,estimated_tokens,objective,acceptance,blocked_reason FROM work_items WHERE goal_id=? AND type=? AND status NOT IN ('DONE','DISCARDED') ORDER BY id LIMIT 1`, goalID, "INTEGRATION_FIX").
		Scan(&item.ID, &item.GoalID, &item.MilestoneID, &item.Type, &item.Title, &item.Priority, &item.Status,
			&item.Risk, &item.ChangeScope, &item.Weight, &item.EstimatedTokens, &item.Objective, &item.Acceptance, &item.BlockedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

// RecordIntegrationFailure files the work needed to make the merged result
// work. Two branches that each verified in isolation say nothing about their
// combination, so when the combination fails there is real work to do — and
// leaving it as an error message means nobody is assigned to it.
func (s *Store) RecordIntegrationFailure(ctx context.Context, projectID, goalID, commitSHA string, failing []string) (model.WorkItem, bool, error) {
	detail := strings.Join(failing, ", ")
	if detail == "" {
		detail = "필수 게이트"
	}
	existing, err := s.OpenIntegrationRepair(ctx, goalID)
	switch {
	case err == nil:
		// Another failure on the same unresolved integration is the same
		// problem, so the item is updated rather than duplicated.
		_, err = s.db.ExecContext(ctx, `UPDATE work_items SET acceptance=?,blocked_reason='' WHERE id=?`,
			integrationAcceptance(detail, commitSHA), existing.ID)
		return existing, false, err
	case !errors.Is(err, ErrNotFound):
		return existing, false, err
	}
	item, err := s.CreateWorkItem(ctx, model.WorkItem{GoalID: goalID, Type: "INTEGRATION_FIX",
		Title:       "통합 검증 실패 수정: " + detail,
		Objective:   "각 작업은 격리된 worktree 에서 통과했지만 병합 결과가 " + detail + " 에서 실패했습니다.",
		Acceptance:  integrationAcceptance(detail, commitSHA),
		ChangeScope: "", Priority: 100, Weight: 1, Status: "APPROVED"})
	if err != nil {
		return item, false, err
	}
	return item, true, nil
}

func integrationAcceptance(detail, commitSHA string) string {
	return "기본 브랜치에서 " + detail + " 이(가) 통과한다 (실패 시점 " + shortSHA(commitSHA) + "). goalforge verify integration 으로 확인."
}
