package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/evaluation"
	"github.com/goalforge/goalforge/internal/model"
)

func armsFixture(t *testing.T) (context.Context, *Store, string) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	evaluationCase, err := s.AddEvaluationCase(ctx, EvaluationCase{ProjectID: p.ID, Name: "case", Kind: "feature",
		GoalTitle: "t", GoalObjective: "o"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, s, evaluationCase.ID
}

func record(t *testing.T, ctx context.Context, s *Store, caseID, arm, label, condition, status string, cost float64) {
	t.Helper()
	trial := evaluation.Trial{CaseID: caseID, Label: label, Arm: arm, Repetition: 1,
		ConditionHash: condition, Status: status, Completed: status == evaluation.StatusPassed, CostUSD: cost}
	if err := s.RecordTrial(ctx, trial); err != nil {
		t.Fatal(err)
	}
}

// The claim "GoalForge completes N% of tasks" is not a claim about GoalForge
// unless something else was measured the same way. With no baseline recorded,
// the comparison must say so rather than present GoalForge's own number as a
// result.
func TestNoBaselineIsReportedAsNotComparable(t *testing.T) {
	ctx, s, caseID := armsFixture(t)
	for i := 0; i < 3; i++ {
		record(t, ctx, s, caseID, evaluation.ArmGoalForge, "v1", "COND-A", evaluation.StatusPassed, 1)
	}
	comparisons, err := s.CompareArms(ctx, "PRJ-1", caseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comparisons) != 1 {
		t.Fatalf("comparisons=%+v", comparisons)
	}
	if comparisons[0].Comparable {
		t.Fatal("a result with nothing to compare against must not be called comparable")
	}
	if comparisons[0].PassRateDelta != 0 {
		t.Fatalf("no delta may be produced without a baseline: %v", comparisons[0].PassRateDelta)
	}
	if comparisons[0].Reason == "" {
		t.Fatal("the user must be told why no comparison is available")
	}
}

// With both arms present under one condition, the difference is what the whole
// exercise is for.
func TestBothArmsUnderOneConditionProduceADifference(t *testing.T) {
	ctx, s, caseID := armsFixture(t)
	for i := 0; i < 4; i++ {
		record(t, ctx, s, caseID, evaluation.ArmGoalForge, "v1", "COND-A", evaluation.StatusPassed, 2)
	}
	record(t, ctx, s, caseID, evaluation.ArmBaseline, "v1", "COND-A", evaluation.StatusPassed, 1)
	for i := 0; i < 3; i++ {
		record(t, ctx, s, caseID, evaluation.ArmBaseline, "v1", "COND-A", evaluation.StatusFailed, 1)
	}
	comparisons, err := s.CompareArms(ctx, "PRJ-1", caseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comparisons) != 1 || !comparisons[0].Comparable {
		t.Fatalf("the two arms share a condition: %+v", comparisons)
	}
	// GoalForge 100%, baseline 25%.
	if comparisons[0].PassRateDelta != 75 {
		t.Fatalf("delta=%v", comparisons[0].PassRateDelta)
	}
	// GoalForge: $8 over 4 successes = $2. Baseline: $4 over 1 success = $4.
	if got := comparisons[0].CostPerSuccessRatio; got != 0.5 {
		t.Fatalf("GoalForge succeeded more cheaply per success: ratio=%v", got)
	}
}

// Two arms run under different conditions are not a baseline comparison. The
// difference must be refused rather than reported, because a reported number
// is a number someone will quote.
func TestArmsUnderDifferentConditionsAreNotCompared(t *testing.T) {
	ctx, s, caseID := armsFixture(t)
	record(t, ctx, s, caseID, evaluation.ArmGoalForge, "v1", "COND-A", evaluation.StatusPassed, 1)
	record(t, ctx, s, caseID, evaluation.ArmBaseline, "v1", "COND-B", evaluation.StatusFailed, 1)
	comparisons, err := s.CompareArms(ctx, "PRJ-1", caseID)
	if err != nil {
		t.Fatal(err)
	}
	for _, comparison := range comparisons {
		if comparison.Comparable {
			t.Fatalf("different conditions must not be compared: %+v", comparison)
		}
		if comparison.PassRateDelta != 0 {
			t.Fatalf("no delta across conditions: %+v", comparison)
		}
	}
}

// A group whose own trials were run under different conditions cannot be
// summarized into one number, and must say so.
func TestMixedConditionsWithinAGroupAreFlagged(t *testing.T) {
	ctx, s, caseID := armsFixture(t)
	record(t, ctx, s, caseID, evaluation.ArmGoalForge, "v1", "COND-A", evaluation.StatusPassed, 1)
	record(t, ctx, s, caseID, evaluation.ArmGoalForge, "v1", "COND-B", evaluation.StatusPassed, 1)
	summaries, err := s.CompareTrials(ctx, "PRJ-1", caseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || !summaries[0].MixedConditions {
		t.Fatalf("averaging across conditions must be flagged: %+v", summaries)
	}
}

// Trials recorded before arms existed were GoalForge's own, and must keep
// counting as such rather than becoming a phantom third arm.
func TestTrialsPredatingArmsCountAsGoalForge(t *testing.T) {
	ctx, s, caseID := armsFixture(t)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_trials(id,case_id,label,repetition,condition_hash,status,completed,tokens,cost_usd,interventions,duration_seconds,created_at) VALUES('T-OLD',?,'v1',1,'COND-A','PASSED',1,0,1,0,0,'now')`, caseID); err != nil {
		t.Fatal(err)
	}
	summaries, err := s.CompareTrials(ctx, "PRJ-1", caseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Arm != evaluation.ArmGoalForge {
		t.Fatalf("old trials belong to the GoalForge arm: %+v", summaries)
	}
}
