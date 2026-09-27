package verification

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/testscript"
)

func executable(t *testing.T, dir, name, posix, windows string) string {
	t.Helper()
	return testscript.Write(t, dir, name, posix, windows)
}

func verificationFixture(t *testing.T) (context.Context, *store.Store, model.Project, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: root, DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, goal.ID, work.ID, "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, store.RunRecord{ID: "R1", ProjectID: project.ID, WorkItemID: work.ID, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, "R1", "VERIFYING", "VERIFYING"); err != nil {
		t.Fatal(err)
	}
	return ctx, s, project, "R1"
}

func TestVerificationCompletesGoalOnlyWithEvidence(t *testing.T) {
	ctx, s, project, runID := verificationFixture(t)
	defer s.Close()
	command := executable(t, project.RepositoryPath, "pass", "echo build-ok", "echo build-ok")
	engine, _ := New(s, 1024)
	report, err := engine.Verify(ctx, runID, project, []Gate{{Type: "build_passed", Command: []string{command}, Timeout: time.Second, Required: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || !report.GoalCompleted || report.Progress != 100 {
		t.Fatalf("report=%+v", report)
	}
	got, err := s.ProjectByID(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "COMPLETED" {
		t.Fatalf("state=%s", got.State)
	}
}

func TestVerificationFailureRequiresRepair(t *testing.T) {
	ctx, s, project, runID := verificationFixture(t)
	defer s.Close()
	command := executable(t, project.RepositoryPath, "fail", "echo broken\nexit 2", "echo broken\nexit /b 2")
	engine, _ := New(s, 1024)
	report, err := engine.Verify(ctx, runID, project, []Gate{{Type: "build_passed", Command: []string{command}, Timeout: time.Second, Required: true}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.GoalCompleted || report.Results[0].ExitCode != 2 {
		t.Fatalf("report=%+v", report)
	}
	got, err := s.ProjectByID(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "REPAIR_REQUIRED" {
		t.Fatalf("state=%s", got.State)
	}
}

func TestVerificationTimeoutAndOutputLimit(t *testing.T) {
	ctx, s, project, runID := verificationFixture(t)
	defer s.Close()
	command := executable(t, project.RepositoryPath, "slow", "printf '1234567890'\nsleep 2", "echo 1234567890\nping -n 3 127.0.0.1 > nul")
	engine, _ := New(s, 5)
	report, err := engine.Verify(ctx, runID, project, []Gate{{Type: "build_passed", Command: []string{command}, Timeout: 20 * time.Millisecond, Required: true}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Results[0].Status != "TIMEOUT" || report.Results[0].Output != "12345\n[output truncated]" {
		t.Fatalf("result=%+v", report.Results[0])
	}
}

func TestVerificationBlocksDangerousExecutable(t *testing.T) {
	ctx, s, project, runID := verificationFixture(t)
	defer s.Close()
	engine, _ := New(s, 1024)
	report, err := engine.Verify(ctx, runID, project, []Gate{{Type: "build_passed", Command: []string{"rm", "-rf", "."}, Timeout: time.Second, Required: true}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.Results[0].Status != "FAILED" {
		t.Fatalf("report=%+v", report)
	}
}

// A gate with a value pattern records what it measured, and a measurement
// below the threshold fails even though the command exited zero. Before this
// the engine recorded the gate's own SuccessValue, so a coverage criterion
// was satisfied by configuration rather than by evidence.
func TestGateMeasuresValueAgainstThreshold(t *testing.T) {
	for _, tc := range []struct {
		name, output, pattern, success, wantStatus, wantValue string
	}{
		{"above threshold", "total: (statements) 91.4%", `\(statements\)\s+([0-9.]+)%`, "85", "PASSED", "91.4"},
		{"below threshold", "total: (statements) 71.4%", `\(statements\)\s+([0-9.]+)%`, "85", "FAILED", "71.4"},
		{"no measurement in output", "nothing to report", `coverage:\s+([0-9.]+)`, "85", "FAILED", ""},
		{"not a number", "coverage: unknown", `coverage:\s+(\S+)`, "85", "FAILED", "unknown"},
		{"boolean gate keeps its meaning", "done", "", "true", "PASSED", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := Result{Type: "coverage", Status: "PASSED", Output: tc.output}
			actual := measure(Gate{Type: "coverage", SuccessValue: tc.success, ValuePattern: tc.pattern}, &result)
			if result.Status != tc.wantStatus || actual != tc.wantValue {
				t.Fatalf("status=%s value=%q want status=%s value=%q", result.Status, actual, tc.wantStatus, tc.wantValue)
			}
		})
	}
}

// A failing gate is never credited with a measurement.
func TestMeasureIgnoresFailedGates(t *testing.T) {
	result := Result{Type: "coverage", Status: "FAILED", Output: "total: (statements) 91.4%"}
	if actual := measure(Gate{Type: "coverage", SuccessValue: "85", ValuePattern: `([0-9.]+)%`}, &result); actual != "false" {
		t.Fatalf("actual=%q", actual)
	}
}

// AT-11 end to end: the screen is built and the build gate is green, but the
// save path is a stub, so the journey gate that actually saves a note fails.
// The goal must not be judged complete, and the criterion must report that the
// build's success says nothing about it.
func TestBuildGreenWithFailingJourneyDoesNotCompleteTheGoal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	project := model.Project{ID: "P1", Name: "notes", RepositoryPath: root, DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, project.ID, "notes", "user can save a note", "", []model.Criterion{
		{Type: "build_passed", ExpectedValue: "true", RequiredKind: "build"},
		{Type: "note_saves", ExpectedValue: "true", RequiredKind: "journey"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "save screen"}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, goal.ID, "W1", "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, store.RunRecord{ID: "R1", ProjectID: project.ID, WorkItemID: "W1", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, "R1", "VERIFYING", "VERIFYING"); err != nil {
		t.Fatal(err)
	}
	build := executable(t, root, "build", "echo compiled", "echo compiled")
	// The journey exercises the real save path, which is still a stub.
	journey := executable(t, root, "journey", "echo saving...\necho save handler is a stub\nexit 1", "echo saving...\r\necho save handler is a stub\r\nexit /b 1")
	engine, _ := New(s, 4096)
	report, err := engine.Verify(ctx, "R1", project, []Gate{
		{Type: "build_passed", Command: []string{build}, Timeout: 5 * time.Second, Required: true, Kind: "build"},
		{Type: "note_saves", Command: []string{journey}, Timeout: 5 * time.Second, Required: true, Kind: "journey"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.GoalCompleted {
		t.Fatalf("a finished screen in front of a stub is not a completed goal: %+v", report)
	}
	criteria, err := s.CriteriaStatus(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	byType := map[string]store.CriterionStatus{}
	for _, criterion := range criteria {
		byType[criterion.Type] = criterion
	}
	if byType["build_passed"].Status != "MET" {
		t.Fatalf("the build really did pass: %+v", byType["build_passed"])
	}
	if byType["note_saves"].Status != "UNMET" || byType["note_saves"].EvidenceKind != "journey" {
		t.Fatalf("the journey ran and failed: %+v", byType["note_saves"])
	}
}

// The same project with the journey gate swapped for a second build command:
// everything passes, and the goal still must not complete, because nothing
// measured whether a note can be saved.
func TestPassingBuildInPlaceOfAJourneyStillBlocksCompletion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	project := model.Project{ID: "P1", Name: "notes", RepositoryPath: root, DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, project.ID, "notes", "user can save a note", "", []model.Criterion{
		{Type: "note_saves", ExpectedValue: "true", RequiredKind: "journey"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "save screen"}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, goal.ID, "W1", "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, store.RunRecord{ID: "R1", ProjectID: project.ID, WorkItemID: "W1", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, "R1", "VERIFYING", "VERIFYING"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, goal.ID, "W1", "DONE"); err != nil {
		t.Fatal(err)
	}
	build := executable(t, root, "build", "echo compiled", "echo compiled")
	engine, _ := New(s, 4096)
	report, err := engine.Verify(ctx, "R1", project, []Gate{
		{Type: "note_saves", Command: []string{build}, Timeout: 5 * time.Second, Required: true, Kind: "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed {
		t.Fatalf("every gate passed: %+v", report)
	}
	if report.GoalCompleted {
		t.Fatal("a compile standing in for the user's task must not complete the goal")
	}
}
