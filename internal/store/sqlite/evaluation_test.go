package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/evaluation"
	"github.com/goalforge/goalforge/internal/model"
)

// A fixed case run under two labels is what makes a configuration change
// assessable; passing means every required gate passed, not that the run
// finished.
func TestEvaluationComparesLabels(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "claude", Model: "haiku"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddEvaluationCase(ctx, EvaluationCase{ProjectID: p.ID, Name: "nil deref", Kind: "nope"}); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
	evaluation, err := s.AddEvaluationCase(ctx, EvaluationCase{ProjectID: p.ID, Name: "nil deref", Kind: "bug_fix",
		GoalTitle: "fix the crash", GoalObjective: "빈 입력에서 패닉이 나지 않는다"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(runID, modelName, gateStatus string, cost float64) {
		t.Helper()
		if _, execErr := s.db.ExecContext(ctx, `INSERT INTO runs(id,project_id,work_item_id,provider,model,state,task_type,config_version,started_at,ended_at) VALUES(?,?,NULL,'claude',?,'COMPLETED','CONTINUE_GOAL','cfg1',?,?)`,
			runID, p.ID, modelName, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)); execErr != nil {
			t.Fatal(execErr)
		}
		if _, execErr := s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,run_id,check_type,status,actual_value,required,created_at) VALUES(?,?,'build_passed',?,'true',1,?)`,
			goal.ID, runID, gateStatus, time.Now().UTC().Format(time.RFC3339Nano)); execErr != nil {
			t.Fatal(execErr)
		}
		if _, execErr := s.db.ExecContext(ctx, `INSERT INTO usage_ledger(run_id,event_key,token_type,amount,cost,created_at) VALUES(?,?,'input',1000,?,?)`,
			runID, runID+"-in", cost, time.Now().UTC().Format(time.RFC3339Nano)); execErr != nil {
			t.Fatal(execErr)
		}
		if _, execErr := s.RecordEvaluationResult(ctx, evaluation.ID, "label-"+modelName, runID); execErr != nil {
			t.Fatal(execErr)
		}
	}
	record("R1", "haiku", "FAILED", 0.01)
	record("R2", "haiku", "PASSED", 0.01)
	record("R3", "sonnet", "PASSED", 0.10)
	record("R4", "sonnet", "PASSED", 0.10)
	summaries, err := s.CompareEvaluations(ctx, p.ID, evaluation.ID)
	if err != nil || len(summaries) != 2 {
		t.Fatalf("summaries=%+v err=%v", summaries, err)
	}
	// Sorted by pass rate: the configuration that actually verified comes first.
	if summaries[0].Label != "label-sonnet" || summaries[0].PassRate != 100 {
		t.Fatalf("best=%+v", summaries[0])
	}
	if summaries[1].Label != "label-haiku" || summaries[1].PassRate != 50 {
		t.Fatalf("worst=%+v", summaries[1])
	}
	if summaries[0].AverageCostUSD <= summaries[1].AverageCostUSD {
		t.Fatalf("cost must be reported alongside the pass rate: %+v", summaries)
	}
}

// Rejections are only a signal once the reason is recorded.
func TestRejectionStats(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	for i, category := range []string{"code_quality", "code_quality", "too_broad", ""} {
		approval, requestErr := s.RequestApproval(ctx, p.ID, ApprovalProtectedFiles, "reason")
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		note := ""
		if category != "" {
			note = "예시 " + string(rune('A'+i))
		}
		if err = s.RejectApprovalWithReason(ctx, p.ID, approval.ID, category, note); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RejectApprovalWithReason(ctx, p.ID, "APR-GHOST", "nonsense", ""); err == nil {
		t.Fatal("an unknown category must be refused")
	}
	stats, err := s.RejectionStats(ctx, p.ID)
	if err != nil || len(stats) != 3 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if stats[0].Category != "code_quality" || stats[0].Count != 2 || len(stats[0].Examples) == 0 {
		t.Fatalf("most common reason first, with examples: %+v", stats[0])
	}
}

// Repetition stability is reported separately from the single-run pass rate: a
// tool that succeeds once and one that succeeds every time are not equally
// trustworthy, and averaging them hides the difference.
func TestCompareTrialsSeparatesStabilityAndInvalidTrials(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "claude"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	flaky, err := s.AddEvaluationCase(ctx, EvaluationCase{ProjectID: p.ID, Name: "flaky", Kind: "bug_fix"})
	if err != nil {
		t.Fatal(err)
	}
	solid, err := s.AddEvaluationCase(ctx, EvaluationCase{ProjectID: p.ID, Name: "solid", Kind: "bug_fix"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(caseID, label, status string, repetition int, cost float64) {
		t.Helper()
		if err := s.RecordTrial(ctx, evaluation.Trial{CaseID: caseID, Label: label, Repetition: repetition,
			ConditionHash: "cond1", Status: status, Completed: status == evaluation.StatusPassed,
			Tokens: 1000, CostUSD: cost, DurationSeconds: 10}); err != nil {
			t.Fatal(err)
		}
	}
	// One case passes every repetition, the other only sometimes.
	for i := 1; i <= 3; i++ {
		record(solid.ID, "candidate", evaluation.StatusPassed, i, 0.10)
	}
	record(flaky.ID, "candidate", evaluation.StatusPassed, 1, 0.10)
	record(flaky.ID, "candidate", evaluation.StatusFailed, 2, 0.10)
	record(flaky.ID, "candidate", evaluation.StatusFailed, 3, 0.10)
	// A trial that could not be measured is neither a pass nor a failure.
	record(flaky.ID, "candidate", evaluation.StatusInvalid, 4, 0)

	summaries, err := s.CompareTrials(ctx, p.ID, "")
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summaries=%+v err=%v", summaries, err)
	}
	summary := summaries[0]
	if summary.Trials != 7 || summary.Passed != 4 || summary.Failed != 2 || summary.Invalid != 1 {
		t.Fatalf("counts=%+v", summary)
	}
	// The rate is over measured trials only: 4 of 6.
	if summary.PassRate < 66 || summary.PassRate > 67 {
		t.Fatalf("pass rate must exclude the unmeasurable trial: %.1f", summary.PassRate)
	}
	// One of two cases passed every repetition.
	if summary.CasesRun != 2 || summary.StableCases != 50 {
		t.Fatalf("stability=%+v", summary)
	}
	if summary.CostPerSuccessUSD <= 0 {
		t.Fatalf("cost per success must include the failed attempts: %+v", summary)
	}
}

// A case has to carry enough to be re-executed; storing and loading it must
// round-trip without losing the gates or limits that define the condition.
func TestCaseSpecRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "claude"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	created, err := s.AddEvaluationCase(ctx, EvaluationCase{ProjectID: p.ID, Name: "case", Kind: "feature",
		GoalTitle: "ship", GoalObjective: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	spec := evaluation.CaseSpec{Fixture: "/fixture", Ref: "abc123", CleanTreeID: "tree-1",
		Criteria:    []evaluation.Criterion{{Type: "build_passed", ExpectedValue: "true"}},
		Gates:       []evaluation.Gate{{Type: "build_passed", Command: []string{"go", "build", "./..."}, Required: true, SuccessValue: "true", Timeout: 60}},
		TokenBudget: 50000, CostBudgetUSD: 1.5, TimeoutSeconds: 900}
	if err = s.SaveCaseSpec(ctx, created.ID, spec); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.CaseSpec(ctx, p.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fixture != spec.Fixture || loaded.Ref != spec.Ref || loaded.CleanTreeID != spec.CleanTreeID {
		t.Fatalf("fixture lost: %+v", loaded)
	}
	if len(loaded.Gates) != 1 || loaded.Gates[0].Command[1] != "build" || !loaded.Gates[0].Required {
		t.Fatalf("gates lost: %+v", loaded.Gates)
	}
	if loaded.TokenBudget != 50000 || loaded.CostBudgetUSD != 1.5 || loaded.TimeoutSeconds != 900 {
		t.Fatalf("limits lost: %+v", loaded)
	}
	// The condition hash is what keeps results from different setups apart.
	if evaluation.ConditionHash(loaded) != evaluation.ConditionHash(spec) {
		t.Fatal("a round-tripped spec must hash to the same condition")
	}
	if _, err = s.CaseSpec(ctx, p.ID, "EVAL-GHOST"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown case: %v", err)
	}
}
