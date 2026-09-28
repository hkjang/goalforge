package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/evaluation"
	"github.com/goalforge/goalforge/internal/model"
)

// FailureCase is an evaluation case built from something that actually went
// wrong, together with why it was built.
//
// The improvement loop only closes when a real failure becomes a repeatable
// measurement. Until then "we fixed the prompt" is a claim about a run that
// can never be run again: the repository has moved, the work item is gone, and
// the only record is a log. Pinning the failure turns it into a question the
// next configuration can be asked.
type FailureCase struct {
	Case evaluation.CaseSpec
	// Origin says what this case was built from: a run that failed, or an
	// approval a person turned down.
	Origin string
	// FailureKind is how the gate failure was classified, and
	// RejectionCategory is why the person said no. They are the "what kind of
	// failure" half of the record; without it a suite of cases says a
	// configuration is worse without saying at what.
	FailureKind, RejectionCategory string
	// Expectation states what passing this case would demonstrate, so a later
	// green result means something specific rather than "it ran".
	Expectation string
}

// Origins a failure case can be built from.
const (
	FailureOriginRun      = "run"
	FailureOriginApproval = "approval"
)

// ErrRunSucceeded is returned when a case is requested from a run that did not
// fail. Building one is still allowed, but it has to be asked for: a suite
// silently full of passing cases measures nothing and looks like coverage.
var ErrRunSucceeded = errors.New("this run did not fail")

// BuildCaseFromRun turns a failed run into a re-runnable evaluation case.
//
// The fixture is pinned to the commit the run *started* from, not to whatever
// the repository looks like now. A case pinned to the current state asks a
// different question — one where the fix may already be present — and would
// pass for reasons that have nothing to do with the configuration under test.
func (s *Store) BuildCaseFromRun(ctx context.Context, projectID, runID string, allowSucceeded bool) (FailureCase, error) {
	var result FailureCase
	pkg, err := s.BuildReproductionPackage(ctx, projectID, runID)
	if err != nil {
		return result, err
	}
	failed := pkg.State == "FAILED" || pkg.State == "REPAIR_REQUIRED"
	var failureKind string
	for _, record := range pkg.Results {
		if record.Status != "PASSED" && record.Required {
			failed = true
			if failureKind == "" {
				failureKind = record.FailureKind
			}
		}
	}
	if !failed && !allowSucceeded {
		return result, ErrRunSucceeded
	}
	if strings.TrimSpace(pkg.BaseCommit) == "" {
		// Without the starting state there is no task to re-run, only a
		// description of one. Refusing is better than pinning to HEAD and
		// calling the result a measurement.
		return result, errors.New("이 실행은 시작 시점의 커밋을 기록하지 않아 재현 가능한 과제로 만들 수 없습니다")
	}
	result.Origin, result.FailureKind = FailureOriginRun, failureKind
	result.Case, err = s.caseSpecFrom(ctx, projectID, pkg)
	if err != nil {
		return result, err
	}
	result.Expectation = expectationFor(pkg.Results, failureKind)
	return result, nil
}

// BuildCaseFromApproval turns a rejected approval into a case. A person
// refusing work is a failure the gates did not catch, which makes it the more
// valuable kind to pin.
func (s *Store) BuildCaseFromApproval(ctx context.Context, projectID, approvalID string) (FailureCase, error) {
	var result FailureCase
	approval, err := s.ApprovalByID(ctx, projectID, approvalID)
	if err != nil {
		return result, err
	}
	if approval.Status != "REJECTED" {
		return result, fmt.Errorf("승인 %s 은(는) 반려되지 않았습니다 (현재 %s)", approvalID, approval.Status)
	}
	if approval.Scope.WorkItemID == "" {
		return result, errors.New("이 반려는 특정 작업에 묶여 있지 않아 과제로 만들 수 없습니다")
	}
	runID, err := s.latestRunForWork(ctx, projectID, approval.Scope.WorkItemID)
	if err != nil {
		return result, err
	}
	pkg, err := s.BuildReproductionPackage(ctx, projectID, runID)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(pkg.BaseCommit) == "" {
		return result, errors.New("이 작업의 실행은 시작 시점의 커밋을 기록하지 않아 재현 가능한 과제로 만들 수 없습니다")
	}
	result.Origin = FailureOriginApproval
	result.RejectionCategory, err = s.rejectionCategory(ctx, projectID, approvalID)
	if err != nil {
		return result, err
	}
	result.Case, err = s.caseSpecFrom(ctx, projectID, pkg)
	if err != nil {
		return result, err
	}
	// A rejection says the gates passed and a person still said no, so the
	// expectation cannot be "the gate goes green" — it already was.
	result.Expectation = "게이트는 통과했지만 사람이 반려한 작업입니다. 같은 과제에서 반려 사유(" +
		result.RejectionCategory + ")가 재발하지 않는지 봅니다. 게이트만으로는 판정되지 않으므로 사람의 검토가 필요합니다."
	return result, nil
}

// caseSpecFrom assembles the pinned task from a run's own record: the state it
// started from, what it was asked to do, and what judged it.
func (s *Store) caseSpecFrom(ctx context.Context, projectID string, pkg ReproductionPackage) (evaluation.CaseSpec, error) {
	spec := evaluation.CaseSpec{
		Fixture: pkg.Repository,
		Ref:     pkg.BaseCommit,
	}
	goal, err := s.goalForRun(ctx, projectID, pkg.WorkItemID)
	if err != nil {
		return spec, err
	}
	spec.GoalTitle, spec.GoalObjective = goal.Title, goal.Objective
	for _, criterion := range goal.Criteria {
		spec.Criteria = append(spec.Criteria, evaluation.Criterion{Type: criterion.Type,
			ExpectedValue: criterion.ExpectedValue, RequiredKind: criterion.RequiredKind})
	}
	for _, gate := range pkg.Gates {
		spec.Gates = append(spec.Gates, evaluation.Gate{Type: gate.Type, Command: gate.Command,
			Required: gate.Required, SuccessValue: gate.SuccessValue, ValuePattern: gate.ValuePattern,
			Timeout: int(gate.Timeout / time.Second), Kind: gate.Kind})
	}
	if pkg.WorkItemID != "" {
		work, workErr := s.WorkItemByID(ctx, goal.ID, pkg.WorkItemID)
		if workErr == nil {
			// The work item is seeded so the case measures implementation
			// rather than decomposition: the failure happened while doing this
			// piece, and re-deriving a different piece would measure something
			// else.
			spec.SeedWork = []evaluation.SeedWorkItem{{Title: work.Title, Objective: work.Objective,
				Acceptance: work.Acceptance, ChangeScope: work.ChangeScope, Priority: work.Priority,
				EstimatedTokens: work.EstimatedTokens}}
		} else if !errors.Is(workErr, ErrNotFound) {
			return spec, workErr
		}
	}
	budget, err := s.ProjectBudgetConfig(ctx, projectID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return spec, err
	}
	spec.TokenBudget, spec.CostBudgetUSD = budget.TokenLimit, budget.CostLimitUSD
	return spec, nil
}

// expectationFor states what a green result would demonstrate.
func expectationFor(results []VerificationRecord, failureKind string) string {
	var failed []string
	for _, record := range results {
		if record.Status != "PASSED" && record.Required {
			failed = append(failed, record.CheckType)
		}
	}
	if len(failed) == 0 {
		return "이 과제가 통과하면 같은 조건에서 작업이 완료됩니다"
	}
	expectation := "이 과제가 통과하려면 " + strings.Join(failed, ", ") + " 이(가) 통과해야 합니다"
	if failureKind != "" {
		expectation += " (원래 실패 유형: " + failureKind + ")"
	}
	return expectation
}

// goalForRun resolves the goal the run was working toward. It prefers the
// work item's own goal over the project's current one, because a case pinned
// to a later goal's criteria would judge the old failure by a standard that
// did not exist when it happened.
func (s *Store) goalForRun(ctx context.Context, projectID, workItemID string) (model.Goal, error) {
	if workItemID != "" {
		var goalID string
		err := s.db.QueryRowContext(ctx, `SELECT goal_id FROM work_items WHERE id=?`, workItemID).Scan(&goalID)
		switch {
		case err == nil:
			return s.loadGoal(ctx, `SELECT id,project_id,version,title,objective,status,change_reason,created_at FROM goals WHERE id=?`, goalID)
		case !errors.Is(err, sql.ErrNoRows):
			return model.Goal{}, err
		}
	}
	goal, err := s.CurrentGoal(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		return s.LatestGoal(ctx, projectID)
	}
	return goal, err
}

func (s *Store) latestRunForWork(ctx context.Context, projectID, workItemID string) (string, error) {
	var runID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM runs WHERE project_id=? AND work_item_id=? ORDER BY started_at DESC,id DESC LIMIT 1`, projectID, workItemID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return runID, err
}

// rejectionCategory reads why the work was turned down. An unrecorded reason
// is reported as such rather than left blank: "we do not know why" is a
// different finding from "there was no reason", and a suite built on the first
// will keep producing cases nobody can act on.
func (s *Store) rejectionCategory(ctx context.Context, projectID, approvalID string) (string, error) {
	var category string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(rejection_category,''),'unrecorded') FROM approvals WHERE id=? AND project_id=?`, approvalID, projectID).Scan(&category)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return category, err
}
