package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func contractCompletionFixture(t *testing.T) (context.Context, *Store, model.Project, model.Goal) {
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
	goal, err := s.SetGoal(ctx, project.ID, "상담 시스템", "문의 접수부터 종료까지", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	// All the work is done and the criterion is met, so nothing but the
	// contract can stand between here and "complete".
	if _, err = s.db.ExecContext(ctx, `INSERT INTO work_items(id,goal_id,type,title,status,weight) VALUES('W1',?,'IMPLEMENT','작업','DONE',1)`, goal.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordVerification(ctx, goal.ID, "build_passed", "PASSED", "true", "ok"); err != nil {
		t.Fatal(err)
	}
	return ctx, s, project, goal
}

// A contract states what the goal requires. An outcome with no way to decide
// it is a requirement nobody has agreed how to settle, so the goal cannot be
// finished — the plan preview said so and the completion judgment did not,
// which meant the warning was advice and the verdict ignored it.
func TestUnconfirmedRequiredOutcomeBlocksCompletion(t *testing.T) {
	ctx, s, project, goal := contractCompletionFixture(t)
	if _, err := s.SaveContract(ctx, GoalContract{ProjectID: project.ID, Title: "상담 시스템",
		Outcomes: []RequiredOutcome{
			{Key: "login", Statement: "상담원이 로그인한다", Method: "gate:auth_tests", Judge: "verification"},
			{Key: "handling_time", Statement: "처리 시간이 줄어든다"},
		}}); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GoalProgressDetail(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Complete {
		t.Fatal("a requirement nobody can settle must not be judged met")
	}
	if len(detail.UnconfirmedOutcomes) != 1 || detail.UnconfirmedOutcomes[0] != "handling_time" {
		t.Fatalf("the blocking outcome must be named: %+v", detail.UnconfirmedOutcomes)
	}
	if !strings.Contains(detail.IncompleteReason, "handling_time") {
		t.Fatalf("the reason must name it: %q", detail.IncompleteReason)
	}
}

// Requirements that cannot both hold mean the goal has no achievable
// definition, whichever way the work went.
func TestConflictingOutcomesBlockCompletion(t *testing.T) {
	ctx, s, project, goal := contractCompletionFixture(t)
	if _, err := s.SaveContract(ctx, GoalContract{ProjectID: project.ID, Title: "성능",
		Outcomes: []RequiredOutcome{
			{Key: "fast", Statement: "빠르다", Method: "gate:latency", Judge: "verification",
				Metric: "latency_ms", Comparator: "<=", Threshold: "200"},
			{Key: "slow_ok", Statement: "여유롭다", Method: "gate:latency", Judge: "verification",
				Metric: "latency_ms", Comparator: ">=", Threshold: "500"},
		}}); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GoalProgressDetail(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Complete {
		t.Fatal("a goal whose requirements contradict cannot be complete")
	}
	if len(detail.OutcomeConflicts) == 0 {
		t.Fatalf("the conflict must be reported: %+v", detail)
	}
}

// A contract every outcome of which can be settled does not get in the way.
func TestFullyConfirmedContractAllowsCompletion(t *testing.T) {
	ctx, s, project, goal := contractCompletionFixture(t)
	if _, err := s.SaveContract(ctx, GoalContract{ProjectID: project.ID, Title: "상담 시스템",
		Outcomes: []RequiredOutcome{
			{Key: "login", Statement: "로그인", Method: "gate:auth_tests", Judge: "verification"},
		}}); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GoalProgressDetail(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Complete {
		t.Fatalf("nothing is blocking: %+v", detail)
	}
}

// A project with no contract behaves as it always did: the contract is an
// additional way to be incomplete, not a new requirement to have one.
func TestNoContractLeavesCompletionUnchanged(t *testing.T) {
	ctx, s, _, goal := contractCompletionFixture(t)
	detail, err := s.GoalProgressDetail(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Complete {
		t.Fatalf("no contract means nothing extra to satisfy: %+v", detail)
	}
}
