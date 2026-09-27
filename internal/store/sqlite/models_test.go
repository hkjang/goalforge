package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func modelFixture(t *testing.T) (context.Context, *Store, model.Project) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "claude", Model: "haiku", FallbackModel: "sonnet"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}}); err != nil {
		t.Fatal(err)
	}
	return ctx, s, p
}

func recordRun(t *testing.T, ctx context.Context, s *Store, projectID, id, modelName, state string, tokens int64, cost float64) {
	t.Helper()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO runs(id,project_id,provider,model,state,task_type,started_at,ended_at) VALUES(?,?,'claude',?,?,'CONTINUE_GOAL',?,?)`,
		id, projectID, modelName, state, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO usage_ledger(run_id,event_key,token_type,amount,cost,created_at) VALUES(?,?,'input',?,0,?)`,
		id, id+"-in", tokens, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO usage_ledger(run_id,event_key,token_type,amount,cost,created_at) VALUES(?,?,'cost_usd',0,?,?)`,
		id, id+"-cost", cost, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

// Model selection is decided by verification outcomes, not by whether the
// provider call returned, and it keeps the configured model until the evidence
// is strong enough to justify moving.
func TestSelectModelForTask(t *testing.T) {
	ctx, s, p := modelFixture(t)
	choice, err := s.SelectModelForTask(ctx, p.ID, p.Model, p.FallbackModel, "CONTINUE_GOAL")
	if err != nil || choice.Model != "haiku" || choice.Source != "configured" {
		t.Fatalf("with no history the configured model stands: %+v err=%v", choice, err)
	}
	// Two runs each is below the evidence threshold.
	recordRun(t, ctx, s, p.ID, "R1", "haiku", "REPAIR_REQUIRED", 1000, 0.01)
	recordRun(t, ctx, s, p.ID, "R2", "sonnet", "COMPLETED", 1000, 0.10)
	if choice, err = s.SelectModelForTask(ctx, p.ID, p.Model, p.FallbackModel, "CONTINUE_GOAL"); err != nil || choice.Model != "haiku" {
		t.Fatalf("thin evidence must not flip the choice: %+v err=%v", choice, err)
	}
	for i := 0; i < 3; i++ {
		recordRun(t, ctx, s, p.ID, fmt.Sprintf("RH%d", i), "haiku", "REPAIR_REQUIRED", 1000, 0.01)
		recordRun(t, ctx, s, p.ID, fmt.Sprintf("RS%d", i), "sonnet", "COMPLETED", 1000, 0.10)
	}
	choice, err = s.SelectModelForTask(ctx, p.ID, p.Model, p.FallbackModel, "CONTINUE_GOAL")
	if err != nil || choice.Model != "sonnet" || choice.Source != "history" {
		t.Fatalf("a model that actually passes verification should win: %+v err=%v", choice, err)
	}
	if choice.Reason == "" || len(choice.Considered) != 2 {
		t.Fatalf("the choice must be explainable: %+v", choice)
	}
	// A model outside the approved set is never chosen.
	if choice, err = s.SelectModelForTask(ctx, p.ID, "haiku", "", "CONTINUE_GOAL"); err != nil || choice.Model != "haiku" {
		t.Fatalf("only approved models may be selected: %+v err=%v", choice, err)
	}
}

// A forecast states a range and how much evidence is behind it.
func TestForecastTokensReportsRangeAndConfidence(t *testing.T) {
	ctx, s, p := modelFixture(t)
	forecast, err := s.ForecastTokens(ctx, p.ID, "CONTINUE_GOAL")
	if err != nil || forecast.Samples != 0 || forecast.Confidence != "none" {
		t.Fatalf("no history: %+v err=%v", forecast, err)
	}
	goal, err := s.CurrentGoal(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "x", EstimatedTokens: 1000}); err != nil {
		t.Fatal(err)
	}
	for i, tokens := range []int64{500, 1000, 1500, 2000, 9000, 1200} {
		id := fmt.Sprintf("RW%d", i)
		if _, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,project_id,work_item_id,provider,model,state,task_type,started_at) VALUES(?,?,'W1','claude','haiku','COMPLETED','CONTINUE_GOAL',?)`,
			id, p.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO usage_ledger(run_id,event_key,token_type,amount,cost,created_at) VALUES(?,?,'input',?,0,?)`,
			id, id+"-in", tokens, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	forecast, err = s.ForecastTokens(ctx, p.ID, "CONTINUE_GOAL")
	if err != nil || forecast.Samples != 6 || forecast.Confidence != "medium" {
		t.Fatalf("forecast=%+v err=%v", forecast, err)
	}
	if forecast.Low >= forecast.Expected || forecast.High <= forecast.Expected {
		t.Fatalf("the range must bracket the expectation: %+v", forecast)
	}
	accuracy, err := s.EstimateAccuracy(ctx, p.ID)
	if err != nil || accuracy.Samples != 6 || accuracy.MeanAbsolutePercent == 0 {
		t.Fatalf("accuracy=%+v err=%v", accuracy, err)
	}
}
