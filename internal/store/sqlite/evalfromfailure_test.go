package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func failureFixture(t *testing.T) (context.Context, *Store, model.Project, model.Goal) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, project.ID, "결제 개선", "재시도를 고친다", "", []model.Criterion{
		{Type: "build_passed", ExpectedValue: "true"},
		{Type: "retry_works", ExpectedValue: "true", RequiredKind: "journey"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpsertGate(ctx, project.ID, GateConfig{Type: "build_passed", Command: []string{"go", "build", "./..."},
		Timeout: 5 * time.Minute, Required: true, SuccessValue: "true", Kind: "build"}); err != nil {
		t.Fatal(err)
	}
	return ctx, s, project, goal
}

// seedFailedRun records a run that started from a known commit and failed a
// required gate, which is the situation this feature exists to capture.
func seedFailedRun(t *testing.T, ctx context.Context, s *Store, project model.Project, goal model.Goal, state string) string {
	t.Helper()
	work, err := s.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT", Title: "재시도 백오프 구현",
		Objective: "지수 백오프", Acceptance: "실패율 1% 미만", ChangeScope: "internal/payment/**", Priority: 90})
	if err != nil {
		t.Fatal(err)
	}
	runID := "RUN-FAIL"
	if err = s.StartRun(ctx, RunRecord{ID: runID, ProjectID: project.ID, WorkItemID: work.ID, Provider: "codex", Model: "haiku"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE runs SET base_commit=? WHERE id=?`, "abcdef1234567890", runID); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordRunVerification(ctx, VerificationRecord{RunID: runID, CheckType: "build_passed",
		Status: "FAILED", ActualValue: "false", Required: true, FailureKind: "build_failure", EvidenceKind: "build"}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, runID, state, state); err != nil {
		t.Fatal(err)
	}
	return runID
}

// The improvement loop only closes when a real failure becomes a repeatable
// measurement. The case has to carry everything needed to re-run the task.
func TestCaseFromFailedRunCarriesTheWholeTask(t *testing.T) {
	ctx, s, project, goal := failureFixture(t)
	runID := seedFailedRun(t, ctx, s, project, goal, "FAILED")
	failure, err := s.BuildCaseFromRun(ctx, project.ID, runID, false)
	if err != nil {
		t.Fatal(err)
	}
	if failure.Origin != FailureOriginRun || failure.FailureKind != "build_failure" {
		t.Fatalf("the kind of failure must be recorded: %+v", failure)
	}
	// Pinned to where the run started, not to wherever the repository is now:
	// a case pinned to the current state asks a different question, one where
	// the fix may already be present.
	if failure.Case.Ref != "abcdef1234567890" {
		t.Fatalf("the case must pin the commit the run started from: %q", failure.Case.Ref)
	}
	if failure.Case.Fixture != project.RepositoryPath {
		t.Fatalf("fixture=%q", failure.Case.Fixture)
	}
	if len(failure.Case.Criteria) != 2 {
		t.Fatalf("the goal's criteria judge the case: %+v", failure.Case.Criteria)
	}
	// The demanded proof kind travels too, or the case would be judged by a
	// weaker standard than the run that failed.
	var journey bool
	for _, criterion := range failure.Case.Criteria {
		if criterion.Type == "retry_works" && criterion.RequiredKind == "journey" {
			journey = true
		}
	}
	if !journey {
		t.Fatalf("the demanded proof kind must survive: %+v", failure.Case.Criteria)
	}
	if len(failure.Case.Gates) != 1 || failure.Case.Gates[0].Kind != "build" {
		t.Fatalf("the gates in force must be pinned: %+v", failure.Case.Gates)
	}
	// Seeding the work item makes the case measure implementation; re-deriving
	// a different piece would measure something else.
	if len(failure.Case.SeedWork) != 1 || failure.Case.SeedWork[0].ChangeScope != "internal/payment/**" {
		t.Fatalf("the failing work item must be seeded: %+v", failure.Case.SeedWork)
	}
	if !strings.Contains(failure.Expectation, "build_passed") {
		t.Fatalf("passing must mean something specific: %q", failure.Expectation)
	}
}

// A suite silently full of passing cases measures nothing and looks like
// coverage, so building one from a success has to be asked for.
func TestCaseFromASuccessfulRunMustBeAskedFor(t *testing.T) {
	ctx, s, project, goal := failureFixture(t)
	work, err := s.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT", Title: "잘 된 작업"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, RunRecord{ID: "RUN-OK", ProjectID: project.ID, WorkItemID: work.ID, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE runs SET base_commit=? WHERE id=?`, "aaaa1111", "RUN-OK"); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordRunVerification(ctx, VerificationRecord{RunID: "RUN-OK", CheckType: "build_passed",
		Status: "PASSED", ActualValue: "true", Required: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, "RUN-OK", "COMPLETED", "COMPLETED"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BuildCaseFromRun(ctx, project.ID, "RUN-OK", false); !errors.Is(err, ErrRunSucceeded) {
		t.Fatalf("a passing run must not silently become a case: %v", err)
	}
	// Asked for explicitly, it becomes a regression guard.
	failure, err := s.BuildCaseFromRun(ctx, project.ID, "RUN-OK", true)
	if err != nil {
		t.Fatalf("an explicit regression guard is allowed: %v", err)
	}
	if failure.Case.Ref != "aaaa1111" {
		t.Fatalf("ref=%q", failure.Case.Ref)
	}
}

// Without the starting state there is no task to re-run, only a description of
// one. Pinning to HEAD instead and calling the result a measurement is the
// failure mode this refuses.
func TestRunWithoutABaselineCannotBecomeACase(t *testing.T) {
	ctx, s, project, goal := failureFixture(t)
	work, err := s.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT", Title: "기준 없는 작업"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, RunRecord{ID: "RUN-NOBASE", ProjectID: project.ID, WorkItemID: work.ID, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordRunVerification(ctx, VerificationRecord{RunID: "RUN-NOBASE", CheckType: "build_passed",
		Status: "FAILED", ActualValue: "false", Required: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, "RUN-NOBASE", "FAILED", "FAILED"); err != nil {
		t.Fatal(err)
	}
	_, err = s.BuildCaseFromRun(ctx, project.ID, "RUN-NOBASE", false)
	if err == nil {
		t.Fatal("a run with no starting commit must be refused")
	}
	if !strings.Contains(err.Error(), "재현") {
		t.Fatalf("the refusal must say why: %v", err)
	}
}

// A person refusing work is a failure the gates did not catch, which makes it
// the more valuable kind to pin — and the reason has to travel with it.
func TestCaseFromRejectionCarriesWhyThePersonSaidNo(t *testing.T) {
	ctx, s, project, goal := failureFixture(t)
	runID := seedFailedRun(t, ctx, s, project, goal, "FAILED")
	_ = runID
	items, err := s.SearchWorkItems(ctx, goal.ID, WorkItemQuery{})
	if err != nil || len(items) == 0 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	approval, err := s.RequestScopedApproval(ctx, project.ID, ApprovalMergeBranch, "병합 요청",
		ApprovalScope{WorkItemID: items[0].ID, CommitSHA: "beef1234", TargetRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RejectApprovalWithReason(ctx, project.ID, approval.ID, "insufficient_evidence", "증거가 부족합니다"); err != nil {
		t.Fatal(err)
	}
	failure, err := s.BuildCaseFromApproval(ctx, project.ID, approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failure.Origin != FailureOriginApproval || failure.RejectionCategory != "insufficient_evidence" {
		t.Fatalf("the rejection category must travel: %+v", failure)
	}
	// A rejection means the gates passed and a person still said no, so the
	// expectation cannot be "the gate goes green".
	if !strings.Contains(failure.Expectation, "사람") {
		t.Fatalf("the expectation must acknowledge that gates alone do not judge this: %q", failure.Expectation)
	}
}

// An approval that was not rejected is not a failure to learn from.
func TestOnlyRejectedApprovalsBecomeCases(t *testing.T) {
	ctx, s, project, _ := failureFixture(t)
	approval, err := s.RequestApproval(ctx, project.ID, ApprovalProtectedFiles, "설정 수정")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BuildCaseFromApproval(ctx, project.ID, approval.ID); err == nil {
		t.Fatal("a pending approval is not a failure")
	}
}
