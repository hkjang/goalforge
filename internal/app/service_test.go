package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/orchestrator"
	"github.com/goalforge/goalforge/internal/planner"
	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/testscript"
	"github.com/goalforge/goalforge/internal/verification"
)

type fakeProvider struct {
	request      provider.RunRequest
	finalMessage string
	onStart      func(provider.RunRequest)
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true}
}
func (f *fakeProvider) Start(_ context.Context, r provider.RunRequest) (<-chan provider.Event, error) {
	f.request = r
	if f.onStart != nil {
		f.onStart(r)
	}
	ch := make(chan provider.Event, 3)
	ch <- provider.Event{Type: provider.EventSessionStarted, SessionID: "session", Raw: json.RawMessage(`{"type":"session"}`)}
	if f.finalMessage != "" {
		ch <- provider.Event{Type: provider.EventMessage, Message: f.finalMessage, Raw: json.RawMessage(`{"type":"message"}`)}
	}
	ch <- provider.Event{Type: provider.EventCompleted, TurnID: "turn", Raw: json.RawMessage(`{"type":"completed"}`)}
	close(ch)
	return ch, nil
}

func TestIdeasRunsIsolatedStructuredDiscovery(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "fake"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "objective", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	fake := &fakeProvider{finalMessage: `{"ideas":[{"title":"Add recovery audit","expected_change_scope":"internal/audit","risk":"low","goal_contribution":90,"user_value":70,"operational_need":80,"feasibility":85,"risk_reduction":75,"difficulty":30,"scope_expansion":false}]}`}
	runner, _ := orchestrator.New(db, fake)
	verify, _ := verification.New(db, 1024)
	service, _ := New(db, plannerService, runner, verify, func() string { return "RUN-I" })
	result, err := service.Ideas(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State != "COMPLETED" || len(result.Discovery.Accepted) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if fake.request.WorkspaceWrite || !fake.request.Ephemeral || fake.request.OutputSchema == "" {
		t.Fatalf("request=%+v", fake.request)
	}
	items, err := db.ListWorkItems(ctx, goal.ID)
	if err != nil || len(items) != 1 || items[0].Title != "Add recovery audit" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if _, err = db.ActiveSession(ctx, project.ID, "fake"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("isolated discovery persisted active session: %v", err)
	}
}
func (f *fakeProvider) Resume(ctx context.Context, _ string, r provider.RunRequest) (<-chan provider.Event, error) {
	return f.Start(ctx, r)
}
func (f *fakeProvider) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	return provider.QuotaSnapshot{}, provider.ErrQuotaUnavailable
}
func (f *fakeProvider) Interrupt(context.Context, string) error { return nil }

func TestContinueSelectsRunsAndVerifiesOneWorkItem(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: root, DefaultBranch: "main", Provider: "fake"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature", Priority: 10, ChangeScope: "generated.go"}); err != nil {
		t.Fatal(err)
	}
	script := testscript.Write(t, root, "verify", "exit 0", "exit /b 0")
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "goalforge@example.invalid"}, {"config", "user.name", "GoalForge Test"}, {"add", filepath.Base(script)}, {"commit", "-m", "verification fixture"}} {
		if output, gitErr := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); gitErr != nil {
			t.Fatalf("git %v: %v: %s", args, gitErr, output)
		}
	}
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed", Command: []string{script}, Timeout: time.Second, Required: true}); err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	fake := &fakeProvider{onStart: func(request provider.RunRequest) {
		if request.WorkDir == root {
			t.Fatal("provider ran in the base repository instead of a worktree")
		}
		if writeErr := os.WriteFile(filepath.Join(request.WorkDir, "generated.go"), []byte("package generated\n"), 0o600); writeErr != nil {
			t.Fatalf("provider write: %v", writeErr)
		}
	}}
	runner, _ := orchestrator.New(db, fake)
	verify, _ := verification.New(db, 1024)
	service, _ := New(db, plannerService, runner, verify, func() string { return "RUN-1" })
	result, err := service.Continue(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkItem.ID != "W1" || !result.Run.Resumed && result.Run.State != "VERIFYING" || !result.Verification.GoalCompleted {
		t.Fatalf("result=%+v", result)
	}
	if !fake.request.WorkspaceWrite {
		t.Fatal("implementation run was not workspace-write")
	}
	if result.Run.RunID != "RUN-1" {
		t.Fatalf("run=%s", result.Run.RunID)
	}
	changes, err := db.ListRunFileChanges(ctx, result.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "generated.go" || changes[0].ChangeType != "ADDED" || changes[0].AfterHash == "" {
		t.Fatalf("file changes=%+v", changes)
	}
	if _, err = os.Stat(filepath.Join(root, "generated.go")); !os.IsNotExist(err) {
		t.Fatalf("provider change leaked into base repository: %v", err)
	}
}

func TestContinueAutoCommitsVerifiedWorkWithTrailers(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: root, DefaultBranch: "main", Provider: "fake", AutoCommitEnabled: true}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature", Priority: 10, ChangeScope: "generated.go"}); err != nil {
		t.Fatal(err)
	}
	script := testscript.Write(t, root, "verify", "exit 0", "exit /b 0")
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "goalforge@example.invalid"}, {"config", "user.name", "GoalForge Test"}, {"add", filepath.Base(script)}, {"commit", "-m", "verification fixture"}} {
		if output, gitErr := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); gitErr != nil {
			t.Fatalf("git %v: %v: %s", args, gitErr, output)
		}
	}
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed", Command: []string{script}, Timeout: time.Second, Required: true}); err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	fake := &fakeProvider{onStart: func(request provider.RunRequest) {
		if writeErr := os.WriteFile(filepath.Join(request.WorkDir, "generated.go"), []byte("package generated\n"), 0o600); writeErr != nil {
			t.Fatalf("provider write: %v", writeErr)
		}
	}}
	runner, _ := orchestrator.New(db, fake)
	verify, _ := verification.New(db, 1024)
	service, _ := New(db, plannerService, runner, verify, func() string { return "RUN-AC" })
	result, err := service.Continue(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verification.Passed {
		t.Fatalf("result=%+v", result)
	}
	recorded, err := db.RunCommitByRun(ctx, "RUN-AC")
	if err != nil || recorded.CommitSHA == "" || recorded.GoalID != goal.ID || recorded.WorkItemID != "W1" || recorded.FilesCommitted != 1 {
		t.Fatalf("recorded=%+v err=%v", recorded, err)
	}
	worktree, err := db.WorktreeForWorkItem(ctx, project.ID, "W1")
	if err != nil {
		t.Fatal(err)
	}
	message, err := exec.Command("git", "-C", worktree.Path, "log", "-1", "--format=%B").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"feature", "Goal-ID: " + goal.ID, "Work-Item-ID: W1", "Run-ID: RUN-AC"} {
		if !strings.Contains(string(message), expected) {
			t.Fatalf("missing %q in commit message %q", expected, message)
		}
	}
	status, err := exec.Command("git", "-C", worktree.Path, "status", "--porcelain").Output()
	if err != nil || strings.TrimSpace(string(status)) != "" {
		t.Fatalf("worktree not clean after auto-commit: %q err=%v", status, err)
	}
}

func TestContinueBlocksAfterRepeatedIdenticalVerificationFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "P-LOOP", Name: "loop", RepositoryPath: root, DefaultBranch: "main", Provider: "fake"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W-LOOP", GoalID: goal.ID, Type: "IMPLEMENT", Title: "repair", Priority: 10, ChangeScope: "attempt.txt"}); err != nil {
		t.Fatal(err)
	}
	script := testscript.Write(t, root, "verify", "echo stable-failure\nexit 1", "echo stable-failure\nexit /b 1")
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed", Command: []string{script}, Timeout: time.Second, Required: true}); err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	// Distinct file content per run keeps no_change and same_change quiet so
	// this test isolates the same_error fingerprint path.
	attempt := 0
	fake := &fakeProvider{onStart: func(request provider.RunRequest) {
		attempt++
		_ = os.WriteFile(filepath.Join(request.WorkDir, "attempt.txt"), []byte(fmt.Sprintf("attempt %d", attempt)), 0o600)
	}}
	runner, _ := orchestrator.New(db, fake)
	verify, _ := verification.New(db, 1024)
	run := 0
	service, _ := New(db, plannerService, runner, verify, func() string {
		run++
		return fmt.Sprintf("RUN-LOOP-%d", run)
	})
	for i := 0; i < 3; i++ {
		result, continueErr := service.Continue(ctx, project)
		if continueErr != nil {
			t.Fatalf("run %d: %v", i+1, continueErr)
		}
		if result.Verification.Passed {
			t.Fatalf("run %d unexpectedly passed", i+1)
		}
	}
	got, err := db.ProjectByID(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "BLOCKED" {
		t.Fatalf("state=%s", got.State)
	}
}

func TestContinueBlocksAfterRepeatedNoChangeFailures(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "P-NOCHANGE", Name: "nochange", RepositoryPath: root, DefaultBranch: "main", Provider: "fake"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W-NC", GoalID: goal.ID, Type: "IMPLEMENT", Title: "repair", Priority: 10}); err != nil {
		t.Fatal(err)
	}
	script := testscript.Write(t, root, "verify", "echo broken\nexit 1", "echo broken\nexit /b 1")
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed", Command: []string{script}, Timeout: time.Second, Required: true}); err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	runner, _ := orchestrator.New(db, &fakeProvider{})
	verify, _ := verification.New(db, 1024)
	run := 0
	service, _ := New(db, plannerService, runner, verify, func() string {
		run++
		return fmt.Sprintf("RUN-NC-%d", run)
	})
	// LOOP-005: two failing runs without any file change block the project
	// before the same_error threshold of three is reached.
	for i := 0; i < 2; i++ {
		result, continueErr := service.Continue(ctx, project)
		if continueErr != nil {
			t.Fatalf("run %d: %v", i+1, continueErr)
		}
		if result.Verification.Passed {
			t.Fatalf("run %d unexpectedly passed", i+1)
		}
	}
	got, err := db.ProjectByID(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "BLOCKED" {
		t.Fatalf("state=%s", got.State)
	}
	if _, err = service.Continue(ctx, project); err == nil {
		t.Fatal("expected blocked project to refuse another run")
	}
}

func TestContinueBlocksEvidenceBasedGoalDriftBeforeVerification(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "P-DRIFT", Name: "drift", RepositoryPath: root, DefaultBranch: "main", Provider: "fake"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W-DRIFT", GoalID: goal.ID, Type: "IMPLEMENT", Title: "session only", ChangeScope: "internal/session/**"}); err != nil {
		t.Fatal(err)
	}
	verifyMarker := filepath.Join(root, "verification-ran")
	script := testscript.Write(t, root, "verify", "touch "+verifyMarker, "type nul > \""+verifyMarker+"\"")
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed", Command: []string{script}, Timeout: time.Second, Required: true}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeProvider{onStart: func(request provider.RunRequest) {
		_ = os.WriteFile(filepath.Join(request.WorkDir, "README.md"), []byte("unrelated"), 0o600)
	}}
	planning, _ := planner.NewService(db, planner.DefaultPolicy())
	runner, _ := orchestrator.New(db, fake)
	verify, _ := verification.New(db, 1024)
	service, _ := New(db, planning, runner, verify, func() string { return "RUN-DRIFT" })
	_, err = service.Continue(ctx, project)
	if err == nil || !strings.Contains(err.Error(), "outside declared scope") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(verifyMarker); !os.IsNotExist(statErr) {
		t.Fatalf("verification ran after drift detection: %v", statErr)
	}
	project, _ = db.ProjectByID(ctx, project.ID)
	items, _ := db.ListWorkItems(ctx, goal.ID)
	if project.State != "BLOCKED" || len(items) != 1 || items[0].Status != "BACKLOG" {
		t.Fatalf("project=%+v items=%+v", project, items)
	}
}

func TestResumePausedValidatesCheckpointAndVerifies(t *testing.T) {
	ctx := context.Background()
	repository := t.TempDir()
	if err := exec.Command("git", "-C", repository, "init", "-b", "main").Run(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: repository, DefaultBranch: "main", Provider: "fake"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	work, err := db.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature", Priority: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetWorkItemStatus(ctx, goal.ID, work.ID, "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (gitops.GitInspector{}).Snapshot(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateCheckpoint(ctx, store.Checkpoint{ProjectID: project.ID, GoalVersion: goal.Version, WorkItemID: work.ID, Provider: project.Provider, CommitSHA: snapshot.CommitSHA, Branch: snapshot.Branch, DirtyFiles: snapshot.DirtyFiles, NextAction: "finish feature"}); err != nil {
		t.Fatal(err)
	}
	if err = db.TransitionProjectState(ctx, project.ID, "READY", "BLOCKED"); err != nil {
		t.Fatal(err)
	}
	script := testscript.Write(t, repository, "verify", "exit 0", "exit /b 0")
	// The checkpoint must include the verification script created above.
	snapshot, err = (gitops.GitInspector{}).Snapshot(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateCheckpoint(ctx, store.Checkpoint{ProjectID: project.ID, GoalVersion: goal.Version, WorkItemID: work.ID, Provider: project.Provider, CommitSHA: snapshot.CommitSHA, Branch: snapshot.Branch, DirtyFiles: snapshot.DirtyFiles, NextAction: "finish feature"}); err != nil {
		t.Fatal(err)
	}
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed", Command: []string{script}, Timeout: time.Second, Required: true}); err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	fake := &fakeProvider{}
	runner, _ := orchestrator.New(db, fake)
	verify, _ := verification.New(db, 1024)
	service, _ := New(db, plannerService, runner, verify, func() string { return "RUN-R" })
	result, err := service.ResumePaused(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State != "VERIFYING" || !result.Verification.Passed || !result.Verification.GoalCompleted {
		t.Fatalf("result=%+v", result)
	}
}

func TestContinueBlocksUnapprovedProtectedFileChange(t *testing.T) {
	ctx := context.Background()
	repository := t.TempDir()
	secretPath := filepath.Join(repository, ".env")
	if err := os.WriteFile(secretPath, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: repository, DefaultBranch: "main", Provider: "fake"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature", Priority: 10}); err != nil {
		t.Fatal(err)
	}
	script := testscript.Write(t, repository, "verify", "exit 0", "exit /b 0")
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed", Command: []string{script}, Timeout: time.Second, Required: true}); err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	fake := &fakeProvider{onStart: func(provider.RunRequest) { _ = os.WriteFile(secretPath, []byte("changed"), 0o600) }}
	runner, _ := orchestrator.New(db, fake)
	verify, _ := verification.New(db, 1024)
	service, _ := New(db, plannerService, runner, verify, func() string { return "RUN-P" })
	if _, err = service.Continue(ctx, project); err == nil || !strings.Contains(err.Error(), "protected files changed") {
		t.Fatalf("err=%v", err)
	}
	project, err = db.ProjectByID(ctx, project.ID)
	if err != nil || project.State != "BLOCKED" {
		t.Fatalf("project=%+v err=%v", project, err)
	}
	items, err := db.ListWorkItems(ctx, goal.ID)
	if err != nil || len(items) != 1 || items[0].Status != "BACKLOG" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	restored, err := os.ReadFile(secretPath)
	if err != nil || string(restored) != "before" {
		t.Fatalf("protected file was not restored: content=%q err=%v", restored, err)
	}
}
