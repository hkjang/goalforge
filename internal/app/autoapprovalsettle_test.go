package app

import (
	"context"
	"fmt"
	"os"
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

// settleFixture is one project with one work item and one required gate whose
// result the test chooses.
func settleFixture(t *testing.T, projectID, itemID, gateBody string) (context.Context, *store.Store, model.Project, *Service) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	project := model.Project{ID: projectID, Name: "demo", RepositoryPath: root,
		DefaultBranch: "main", Provider: "fake"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "ship", "objective", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: itemID, GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "feature", Priority: 10, ChangeScope: "attempt.txt"}); err != nil {
		t.Fatal(err)
	}
	script := testscript.Write(t, root, "verify", gateBody, gateBody)
	for _, args := range [][]string{{"init", "-b", "main"},
		{"config", "user.email", "goalforge@example.invalid"}, {"config", "user.name", "T"},
		{"add", filepath.Base(script)}, {"commit", "-m", "fixture"}} {
		if out, gitErr := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); gitErr != nil {
			t.Skipf("git unavailable: %v %s", gitErr, out)
		}
	}
	if err = db.UpsertGate(ctx, project.ID, store.GateConfig{Type: "build_passed",
		Command: []string{script}, Timeout: time.Second, Required: true}); err != nil {
		t.Fatal(err)
	}
	plannerService, _ := planner.NewService(db, planner.DefaultPolicy())
	attempt := 0
	fake := &fakeProvider{onStart: func(request provider.RunRequest) {
		attempt++
		_ = os.WriteFile(filepath.Join(request.WorkDir, "attempt.txt"),
			[]byte(fmt.Sprintf("attempt %d", attempt)), 0o600)
	}}
	runner, _ := orchestrator.New(db, fake)
	verify, _ := verification.New(db, 1024)
	run := 0
	service, _ := New(db, plannerService, runner, verify, func() string {
		run++
		return fmt.Sprintf("RUN-%d", run)
	})
	return ctx, db, project, service
}

// The autonomy loop refuses to approve an item whose previous automatic
// attempt was settled and failed. Nothing settled one: the only function that
// could had no caller, so every record stayed outstanding and that guard never
// fired in the real flow — the loop was free to re-approve a failing item
// every sweep and spend the day's allowance reaching the same place.
//
// The outcome becomes known here, after verification, so this is where it is
// recorded.
func TestARunSettlesTheAutomaticApprovalItCameFrom(t *testing.T) {
	ctx, db, project, service := settleFixture(t, "P-OK", "W-OK", "exit 0")
	if err := db.RecordAutoApproval(ctx, store.AutoApprovalRecord{WorkItemID: "W-OK",
		ProjectID: project.ID, StandardID: "T-001", Basis: "기준 T-001"}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Continue(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verification.Passed {
		t.Fatalf("the gate exits zero: %+v", result.Verification)
	}
	record, err := db.AutoApprovalFor(ctx, "W-OK")
	if err != nil {
		t.Fatal(err)
	}
	if !record.Settled || !record.Passed {
		t.Fatalf("the attempt ended and passed: %+v", record)
	}
	if record.Outcome == "" {
		t.Fatal("what it did has to survive")
	}
}

// A failure the repair policy will retry on its own is still outstanding.
// Settling it would refuse the retry the policy just granted — and then the
// item would sit refused by a guard meant for attempts that really ended.
func TestARetryableFailureLeavesTheAttemptOutstanding(t *testing.T) {
	ctx, db, project, service := settleFixture(t, "P-RETRY", "W-RETRY", "echo --- FAIL: TestThing\nexit 1")
	if err := db.RecordAutoApproval(ctx, store.AutoApprovalRecord{WorkItemID: "W-RETRY",
		ProjectID: project.ID, StandardID: "T-001", Basis: "b"}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Continue(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verification.Passed {
		t.Fatal("the gate exits one")
	}
	// A test failure is a code fix, so the policy grants a retry. A skip here
	// would cover nothing, and this branch is the whole point of the test.
	if !result.Repair.Automatic() {
		t.Fatalf("a test failure is repairable by changing code: %+v", result.Repair)
	}
	record, err := db.AutoApprovalFor(ctx, "W-RETRY")
	if err != nil {
		t.Fatal(err)
	}
	if record.Settled {
		t.Fatalf("a retry was granted, so the attempt has not ended: %+v", record)
	}
}

// A failure the policy will not retry has ended, and the record says which
// gate stopped it. "verification failed" sends the next reader to the logs.
func TestAnExhaustedFailureIsSettledNamingTheGate(t *testing.T) {
	ctx, db, project, service := settleFixture(t, "P-DONE", "W-DONE", "echo stable-failure\nexit 1")
	if err := db.RecordAutoApproval(ctx, store.AutoApprovalRecord{WorkItemID: "W-DONE",
		ProjectID: project.ID, StandardID: "T-001", Basis: "b"}); err != nil {
		t.Fatal(err)
	}
	// Repeated identical failures exhaust the repair allowance, which is what
	// makes the attempt over rather than outstanding.
	for i := 0; i < 3; i++ {
		if _, err := service.Continue(ctx, project); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	record, err := db.AutoApprovalFor(ctx, "W-DONE")
	if err != nil {
		t.Fatal(err)
	}
	if !record.Settled || record.Passed {
		t.Fatalf("the attempt ended and failed: %+v", record)
	}
	if !strings.Contains(record.Outcome, "build_passed") {
		t.Fatalf("the reason must name what stopped it: %q", record.Outcome)
	}
}

// A person's approval has no automatic record, and a run for it must not fail
// over the missing row.
func TestARunForAManuallyApprovedItemSettlesNothing(t *testing.T) {
	ctx, db, project, service := settleFixture(t, "P-MANUAL", "W-MANUAL", "exit 0")
	if _, err := service.Continue(ctx, project); err != nil {
		t.Fatalf("nothing approved this automatically, which is not an error: %v", err)
	}
	if _, err := db.AutoApprovalFor(ctx, "W-MANUAL"); err == nil {
		t.Fatal("no automatic record should have been invented")
	}
}

// A work item whose scope is a sentence fails every run, and the message has
// to say the scope is the problem.
//
// "changed files outside declared scope: handler.go" sends the reader to look
// at the files when the scope is a paragraph that could never match anything.
// The two failures need different remedies: one is "say the scope as paths",
// the other is "you changed the wrong files". Items filed before the scope
// form was checked are still on boards, so this is what they will say.
func TestAnUnusableScopeBlamesTheScopeNotTheFiles(t *testing.T) {
	ctx, db, project, service := settleFixture(t, "P-SCOPE", "W-SCOPE", "exit 0")
	goal, err := db.CurrentGoal(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W-PROSE", GoalID: goal.ID,
		Type: "IMPLEMENT", Title: "prose scope", Priority: 99,
		ChangeScope: "handler.go 신설: JSON 본문 파싱, 잘못된 입력은 400"}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Continue(ctx, project)
	if err == nil {
		t.Fatal("a scope that matches nothing cannot be satisfied")
	}
	if !strings.Contains(err.Error(), "경로") {
		t.Fatalf("the message must point at the scope: %v", err)
	}
	if strings.Contains(err.Error(), "outside declared scope") {
		t.Fatalf("that message is for files that missed a usable scope: %v", err)
	}
}

// An empty scope is restrictive, not malformed. OutOfScopeChanges reads it as
// "may change nothing" — a deliberate decision — so it gets the file-list
// message, not the one about the form. Conflating them would tell an operator
// who declared no scope on purpose to go and fix a typo.
func TestAnEmptyScopeIsStillTheFileListFailure(t *testing.T) {
	ctx, db, project, service := settleFixture(t, "P-EMPTY", "W-EMPTY", "exit 0")
	goal, err := db.CurrentGoal(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W-NOSCOPE", GoalID: goal.ID,
		Type: "IMPLEMENT", Title: "no scope", Priority: 99, ChangeScope: ""}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Continue(ctx, project)
	if err == nil {
		t.Fatal("an empty scope may change nothing, and the session wrote a file")
	}
	if !strings.Contains(err.Error(), "outside declared scope") {
		t.Fatalf("this is the file-list failure: %v", err)
	}
	if strings.Contains(err.Error(), "경로 목록이 아닙니다") {
		t.Fatalf("an empty scope is not malformed: %v", err)
	}
}
