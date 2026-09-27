package sqlite

import (
	"context"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

// EvidenceBundle is the case for what a goal achieved and how it was proven,
// assembled from the records that were written as it happened. It is what a
// handover, a release review, or an audit actually needs: not "the tests
// passed" but which command measured what, against which commit, approved by
// whom, and what was turned down along the way.
type EvidenceBundle struct {
	GeneratedAt time.Time
	Project     model.Project
	Goal        model.Goal
	GoalHistory []GoalVersion
	Progress    ProgressDetail
	Criteria    []CriterionStatus
	WorkItems   []EvidenceWorkItem
	Decisions   []DesignDecision
	Approvals   []EvidenceApproval
	Relaxations []VerificationRelaxation
	Integration IntegrationCheck
	Cost        ProjectMetrics
}

// GoalVersion is one recorded statement of what done meant.
type GoalVersion struct {
	Version      int
	Title        string
	Objective    string
	Status       string
	ChangeReason string
	Criteria     []model.Criterion
	CreatedAt    time.Time
}

// EvidenceWorkItem is one unit of work with the commit and gate results that
// justify calling it done.
type EvidenceWorkItem struct {
	Item          model.WorkItem
	Commit        *RunCommit
	Verifications []VerificationRecord
	Runs          int
	Tokens        int64
	CostUSD       float64
}

// EvidenceApproval is a decision a person made, including the ones they
// refused: a record that only keeps approvals describes a different project
// than the one that happened.
type EvidenceApproval struct {
	Approval
	RejectionCategory string
	RejectionNote     string
}

// BuildEvidenceBundle assembles the bundle for a project's current goal.
func (s *Store) BuildEvidenceBundle(ctx context.Context, projectID string) (EvidenceBundle, error) {
	bundle := EvidenceBundle{GeneratedAt: time.Now().UTC()}
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		return bundle, err
	}
	bundle.Project = project
	goal, err := s.CurrentGoal(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		goal, err = s.LatestGoal(ctx, projectID)
	}
	if err != nil {
		return bundle, err
	}
	bundle.Goal = goal
	if bundle.GoalHistory, err = s.goalHistory(ctx, projectID); err != nil {
		return bundle, err
	}
	if bundle.Progress, err = s.GoalProgressDetail(ctx, goal); err != nil {
		return bundle, err
	}
	bundle.Criteria = bundle.Progress.Criteria
	items, err := s.ListWorkItems(ctx, goal.ID)
	if err != nil {
		return bundle, err
	}
	for _, item := range items {
		entry := EvidenceWorkItem{Item: item}
		if commit, commitErr := s.LatestRunCommitForWork(ctx, projectID, item.ID); commitErr == nil {
			entry.Commit = &commit
			if entry.Verifications, err = s.VerificationsForRun(ctx, commit.RunID); err != nil {
				return bundle, err
			}
		} else if !errors.Is(commitErr, ErrNotFound) {
			return bundle, commitErr
		}
		runs, runErr := s.RunsForWorkItem(ctx, projectID, item.ID, 100)
		if runErr != nil {
			return bundle, runErr
		}
		entry.Runs = len(runs)
		for _, run := range runs {
			entry.Tokens += run.Tokens
			entry.CostUSD += run.CostUSD
		}
		bundle.WorkItems = append(bundle.WorkItems, entry)
	}
	if bundle.Decisions, err = s.ListDecisions(ctx, projectID, true); err != nil {
		return bundle, err
	}
	if bundle.Approvals, err = s.evidenceApprovals(ctx, projectID); err != nil {
		return bundle, err
	}
	if bundle.Relaxations, err = s.ListRelaxations(ctx, projectID, 200); err != nil {
		return bundle, err
	}
	if bundle.Integration, err = s.IntegrationStatus(ctx, projectID); err != nil {
		return bundle, err
	}
	bundle.Cost, err = s.ProjectMetrics(ctx, projectID)
	return bundle, err
}

func (s *Store) goalHistory(ctx context.Context, projectID string) ([]GoalVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,version,title,objective,status,change_reason,created_at FROM goals WHERE project_id=? ORDER BY version`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		id      string
		version GoalVersion
	}
	var collected []row
	for rows.Next() {
		var entry row
		var created string
		if err = rows.Scan(&entry.id, &entry.version.Version, &entry.version.Title, &entry.version.Objective,
			&entry.version.Status, &entry.version.ChangeReason, &created); err != nil {
			return nil, err
		}
		entry.version.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		collected = append(collected, entry)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := make([]GoalVersion, 0, len(collected))
	for _, entry := range collected {
		criteria, criteriaErr := s.criteriaForGoal(ctx, entry.id)
		if criteriaErr != nil {
			return nil, criteriaErr
		}
		entry.version.Criteria = criteria
		result = append(result, entry.version)
	}
	return result, nil
}

func (s *Store) criteriaForGoal(ctx context.Context, goalID string) ([]model.Criterion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT criterion_type,expected_value FROM goal_criteria WHERE goal_id=? ORDER BY criterion_type`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.Criterion
	for rows.Next() {
		var criterion model.Criterion
		if err = rows.Scan(&criterion.Type, &criterion.ExpectedValue); err != nil {
			return nil, err
		}
		result = append(result, criterion)
	}
	return result, rows.Err()
}

func (s *Store) evidenceApprovals(ctx context.Context, projectID string) ([]EvidenceApproval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,action_type,reason,status,requested_at,COALESCE(approved_at,''),COALESCE(consumed_run_id,''),work_item_id,source_branch,target_ref,commit_sha,files_changed,COALESCE(rejection_category,''),COALESCE(rejection_note,'') FROM approvals WHERE project_id=? ORDER BY requested_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EvidenceApproval
	for rows.Next() {
		var entry EvidenceApproval
		var requested, approved string
		if err = rows.Scan(&entry.ID, &entry.ProjectID, &entry.ActionType, &entry.Reason, &entry.Status, &requested, &approved,
			&entry.ConsumedRunID, &entry.Scope.WorkItemID, &entry.Scope.SourceBranch, &entry.Scope.TargetRef,
			&entry.Scope.CommitSHA, &entry.Scope.FilesChanged, &entry.RejectionCategory, &entry.RejectionNote); err != nil {
			return nil, err
		}
		entry.RequestedAt, _ = time.Parse(time.RFC3339Nano, requested)
		entry.ApprovedAt, _ = time.Parse(time.RFC3339Nano, approved)
		result = append(result, entry)
	}
	return result, rows.Err()
}
