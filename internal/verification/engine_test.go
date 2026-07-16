package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func executable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
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
	command := executable(t, project.RepositoryPath, "pass", "echo build-ok")
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
	command := executable(t, project.RepositoryPath, "fail", "echo broken\nexit 2")
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
	command := executable(t, project.RepositoryPath, "slow", "printf '1234567890'\nsleep 2")
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
