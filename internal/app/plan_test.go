package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/orchestrator"
	"github.com/goalforge/goalforge/internal/planner"
	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/testscript"
	"github.com/goalforge/goalforge/internal/verification"
)

func planFixture(t *testing.T) (context.Context, *store.Store, *Service, model.Project, model.Goal) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: root, DefaultBranch: "main", Provider: "fake", Model: "small"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	script := testscript.Write(t, root, "verify", "exit 0", "exit /b 0")
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "t@example.invalid"}, {"config", "user.name", "T"}, {"add", filepath.Base(script)}, {"commit", "-m", "fixture"}} {
		if output, gitErr := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); gitErr != nil {
			t.Skipf("git %v: %v: %s", args, gitErr, output)
		}
	}
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed", Command: []string{script}, Timeout: time.Second, Required: true}); err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	runner, _ := orchestrator.New(db, &fakeProvider{})
	verify, _ := verification.New(db, 1024)
	service, err := New(db, plannerService, runner, verify, func() string { return "RUN-1" })
	if err != nil {
		t.Fatal(err)
	}
	return ctx, db, service, project, goal
}

func checkLevel(plan Plan, name string) string {
	for _, check := range plan.Checks {
		if check.Name == name {
			return check.Level
		}
	}
	return ""
}

// The preview must agree with what a run actually does; a preview that refuses
// work Continue would perform is worse than no preview.
func TestPlanMatchesWhatContinueWouldDo(t *testing.T) {
	ctx, db, service, project, goal := planFixture(t)
	if _, err := db.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "feature", Priority: 10, ChangeScope: "generated.go", EstimatedTokens: 5000}); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Plan(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Runnable() || plan.WorkItem == nil || plan.WorkItem.ID != "W1" {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.SelectionReason == "" {
		t.Fatal("the choice must explain itself")
	}
	// Planning changes nothing: the item is still claimable afterwards.
	claimed, err := db.ClaimNextWorkItem(ctx, goal.ID)
	if err != nil || claimed.ID != "W1" {
		t.Fatalf("a preview must not consume the work it previews: %+v err=%v", claimed, err)
	}
}

// Everything Continue refuses outright is reported as BLOCK, so the preview
// and the run reach the same verdict.
func TestPlanBlocksWhatContinueRefuses(t *testing.T) {
	ctx, db, service, project, goal := planFixture(t)
	// No work item yet.
	plan, err := service.Plan(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Runnable() || checkLevel(plan, "work item") != "BLOCK" {
		t.Fatalf("no work: %+v", plan.Checks)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "f", ChangeScope: "a.go"}); err != nil {
		t.Fatal(err)
	}
	// An exhausted budget is refused by Continue and must be refused here.
	if err = db.SetProjectBudget(ctx, project.ID, 1, 0); err != nil {
		t.Fatal(err)
	}
	// Spend the budget through the same path a provider run would.
	if err = db.StartRun(ctx, store.RunRecord{ID: "R0", ProjectID: project.ID, Provider: "fake"}); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordProviderEvent(ctx, project.ID, provider.Event{RunID: "R0", Type: provider.EventCompleted,
		TurnID: "t0", Raw: json.RawMessage(`{"type":"turn.completed"}`), Usage: &provider.Usage{InputTokens: 50}}); err != nil {
		t.Fatal(err)
	}
	if err = db.FinishRun(ctx, "R0", "COMPLETED", "READY"); err != nil {
		t.Fatal(err)
	}
	plan, err = service.Plan(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Runnable() || checkLevel(plan, "token budget") != "BLOCK" {
		t.Fatalf("exhausted budget: %+v", plan.Checks)
	}
	result, runErr := service.Continue(ctx, project)
	if runErr == nil || !strings.Contains(runErr.Error(), "budget") {
		t.Fatalf("Continue must refuse the same thing: %+v %v", result, runErr)
	}
}

// A readiness problem stops the goal from completing but not the run from
// starting, so it must warn rather than block: the preview would otherwise
// refuse a run that does happen.
func TestPlanWarnsWithoutBlockingOnReadinessProblems(t *testing.T) {
	ctx, db, service, project, goal := planFixture(t)
	if _, err := db.SetGoal(ctx, project.ID, "ship", "objective", "새 조건 추가", []model.Criterion{
		{Type: "build_passed", ExpectedValue: "true"},
		{Type: "latency_p95", ExpectedValue: "200"},
	}); err != nil {
		t.Fatal(err)
	}
	current, err := db.CurrentGoal(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = goal
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: current.ID, Type: "IMPLEMENT", Title: "f", ChangeScope: "a.go"}); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Plan(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Runnable() {
		t.Fatalf("an unmeasurable criterion must not stop the run: %+v", plan.Checks)
	}
	if checkLevel(plan, "readiness: criteria coverage") != "WARN" {
		t.Fatalf("it must still be reported: %+v", plan.Checks)
	}
}
