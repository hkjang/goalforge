package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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
