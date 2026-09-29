package observer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func completedPass(t *testing.T, ctx context.Context, db *store.Store, projectID, sha, trigger string, endedAgo time.Duration) {
	t.Helper()
	run := store.SupplyRun{ProjectID: projectID, CommitSHA: sha, Trigger: trigger}
	if _, _, err := db.BeginSupplyRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishSupplyRun(ctx, run.Key(), store.SupplyRunCompleted, "", store.SupplyCounts{}); err != nil {
		t.Fatal(err)
	}
	if endedAgo > 0 {
		if err := db.BackdateSupplyRunForTest(ctx, run.Key(), time.Now().UTC().Add(-endedAgo)); err != nil {
			t.Fatal(err)
		}
	}
}

func seedRunnable(t *testing.T, ctx context.Context, db *store.Store, goalID string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if _, err := db.CreateWorkItem(ctx, model.WorkItem{GoalID: goalID, Type: "IMPLEMENT",
			Title: "w", Status: "BACKLOG", Weight: 1}); err != nil {
			t.Fatal(err)
		}
	}
}

// Reading the same commit again produces the same answers and spends budget
// doing it. A supplier that re-walks an unchanged repository every hour is
// paying to be told what it already knows.
func TestAnUnchangedRepositoryIsNotReassessed(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	seedRunnable(t, ctx, db, goal.ID, 5)
	completedPass(t, ctx, db, "PRJ-1", "abc123", store.TriggerScheduled, 0)
	decision, err := Due(ctx, db, "PRJ-1", goal.ID, "abc123", DefaultSchedulePolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Run() {
		t.Fatalf("nothing changed: %+v", decision)
	}
	if decision.Reason == "" {
		t.Fatal("a decision not to run must still say why")
	}
}

// A moved branch is new code, and new code is the reason to look again.
func TestAMovedBranchTriggersAPass(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	seedRunnable(t, ctx, db, goal.ID, 5)
	completedPass(t, ctx, db, "PRJ-1", "abc123", store.TriggerScheduled, 0)
	decision, err := Due(ctx, db, "PRJ-1", goal.ID, "def456", DefaultSchedulePolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Trigger != store.TriggerCommitChanged {
		t.Fatalf("decision=%+v", decision)
	}
	if !strings.Contains(decision.Reason, "def456") {
		t.Fatalf("the reason must name the new commit: %q", decision.Reason)
	}
}

// A board with nothing runnable on it is a stalled project, and that is worth
// looking at even though the code has not moved.
func TestAnEmptyBoardTriggersAPassWithoutANewCommit(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	completedPass(t, ctx, db, "PRJ-1", "abc123", store.TriggerScheduled, 0)
	decision, err := Due(ctx, db, "PRJ-1", goal.ID, "abc123", DefaultSchedulePolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Trigger != store.TriggerBacklogLow {
		t.Fatalf("decision=%+v", decision)
	}
}

// Forty items that all wait on one another is an empty board from the runner's
// point of view. A supplier that counted rows would never notice the project
// had stalled.
func TestBlockedWorkDoesNotCountAsRunnable(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	first, err := db.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "first", Status: "BACKLOG", Weight: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err = db.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT",
			Title: "waiter", Status: "BACKLOG", Weight: 1, Dependencies: []string{first.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	runnable, err := db.RunnableWorkCount(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runnable != 1 {
		t.Fatalf("only the one with no predecessor can run: %d", runnable)
	}
}

// A repository can drift away from its criteria without a commit — a pack
// revision, an exception expiring. "Nothing changed" is not "nothing to check".
func TestTheIntervalTriggersAPassEvenWithNoChange(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	seedRunnable(t, ctx, db, goal.ID, 5)
	completedPass(t, ctx, db, "PRJ-1", "abc123", store.TriggerScheduled, 30*time.Hour)
	decision, err := Due(ctx, db, "PRJ-1", goal.ID, "abc123", DefaultSchedulePolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Trigger != store.TriggerScheduled {
		t.Fatalf("decision=%+v", decision)
	}
}

// An exhausted budget and nothing to do look identical from outside and call
// for completely different responses.
func TestAnExhaustedDiscoveryBudgetSaysSo(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	policy := SchedulePolicy{Interval: time.Hour, BacklogFloor: 3, DiscoveryBudget: 2}
	completedPass(t, ctx, db, "PRJ-1", "aaa", store.TriggerScheduled, 0)
	completedPass(t, ctx, db, "PRJ-1", "bbb", store.TriggerScheduled, 0)
	_, err := Due(ctx, db, "PRJ-1", goal.ID, "ccc", policy, time.Now())
	if !errors.Is(err, ErrDiscoveryBudgetSpent) {
		t.Fatalf("an exhausted budget must be said, not silently read as idle: %v", err)
	}
}

// A pass that crashes on every try is exactly the loop a budget exists to
// stop, so every attempt is counted and not only the ones that worked.
func TestFailedPassesSpendTheDiscoveryBudgetToo(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	for _, sha := range []string{"aaa", "bbb"} {
		run := store.SupplyRun{ProjectID: "PRJ-1", CommitSHA: sha, Trigger: store.TriggerScheduled}
		if _, _, err := db.BeginSupplyRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := db.FinishSupplyRun(ctx, run.Key(), store.SupplyRunFailed, "boom", store.SupplyCounts{}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Due(ctx, db, "PRJ-1", goal.ID, "ccc",
		SchedulePolicy{Interval: time.Hour, BacklogFloor: 3, DiscoveryBudget: 2}, time.Now())
	if !errors.Is(err, ErrDiscoveryBudgetSpent) {
		t.Fatalf("failed attempts spend the budget too: %v", err)
	}
}

// A pass that failed did not observe anything, so treating it as the last look
// would skip the project until the next window.
func TestAFailedPassIsNotTheLastLook(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	seedRunnable(t, ctx, db, goal.ID, 5)
	completedPass(t, ctx, db, "PRJ-1", "abc123", store.TriggerScheduled, 0)
	failed := store.SupplyRun{ProjectID: "PRJ-1", CommitSHA: "def456", Trigger: store.TriggerCommitChanged}
	if _, _, err := db.BeginSupplyRun(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishSupplyRun(ctx, failed.Key(), store.SupplyRunFailed, "boom", store.SupplyCounts{}); err != nil {
		t.Fatal(err)
	}
	decision, err := Due(ctx, db, "PRJ-1", goal.ID, "def456",
		SchedulePolicy{Interval: 24 * time.Hour, BacklogFloor: 3}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Trigger != store.TriggerCommitChanged {
		t.Fatalf("the failed attempt did not observe def456: %+v", decision)
	}
}

// A project nobody has ever looked at is due whatever else is true.
func TestAProjectNeverAssessedIsDue(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	seedRunnable(t, ctx, db, goal.ID, 5)
	decision, err := Due(ctx, db, "PRJ-1", goal.ID, "abc123", DefaultSchedulePolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Trigger != store.TriggerScheduled {
		t.Fatalf("decision=%+v", decision)
	}
}

// Two workers seeing the same commit must produce one pass between them, not
// one each. Everything else in the mechanism is pointless if this does not
// hold.
func TestTwoWorkersOnTheSameEventFileOnce(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	req := passRequest(t, ctx, db, goal.ID)
	first, err := RunScheduledPass(ctx, db, req)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Ran || len(first.Result.Filed) == 0 {
		t.Fatalf("the first pass runs and files: %+v", first)
	}
	second, err := RunScheduledPass(ctx, db, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Ran {
		t.Fatalf("the second worker must not run the same event: %+v", second)
	}
	items, err := db.ListWorkItems(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(first.Result.Filed) {
		t.Fatalf("one pass worth of cards, not two: %d", len(items))
	}
}

// The case above stops at the schedule: once the first pass has run there is
// no reason for a second. This one puts two workers on an event that *is* due,
// which is the race the idempotency key exists for. Without it both walk the
// repository, and only the dedup key downstream stops the cards doubling — a
// second line of defence doing the first line's job.
func TestAWorkerStaysOutOfAPassAnotherHasClaimed(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	req := passRequest(t, ctx, db, goal.ID)
	// Another worker got here first and is still going.
	claimed := store.SupplyRun{ProjectID: req.ProjectID, CommitSHA: req.HeadSHA, Trigger: store.TriggerScheduled}
	if _, started, err := db.BeginSupplyRun(ctx, claimed); err != nil || !started {
		t.Fatalf("the other worker claims it: %v %v", started, err)
	}
	result, err := RunScheduledPass(ctx, db, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ran {
		t.Fatal("a pass another worker is running must not be run again")
	}
	if result.Detail == "" {
		t.Fatal("\"somebody else has it\" and \"nothing to do\" must be distinguishable")
	}
	items, err := db.ListWorkItems(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("the second worker must not have filed anything: %d", len(items))
	}
}

// A pass that blows up must leave a finished attempt behind, so the next one
// retries rather than waiting out the abandonment timeout.
func TestACrashedPassIsClosedAsFailed(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	req := passRequest(t, ctx, db, goal.ID)
	req.Repository = "/nonexistent-repository"
	if _, err := RunScheduledPass(ctx, db, req); err == nil {
		t.Fatal("reading a repository that is not there must fail")
	}
	last, err := db.LastSupplyRun(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if last.Status != store.SupplyRunFailed {
		t.Fatalf("a crashed pass must be closed as failed, not left running: %+v", last)
	}
	if last.Detail == "" {
		t.Fatal("the failure must say what happened")
	}
}

// Running the pass records why it ran, so a report can say what the schedule
// is doing without walking the board.
func TestTheRecordedPassSaysWhyItRan(t *testing.T) {
	ctx, db, _, goal := supplyStoreFixture(t)
	if _, err := RunScheduledPass(ctx, db, passRequest(t, ctx, db, goal.ID)); err != nil {
		t.Fatal(err)
	}
	last, err := db.LastCompletedSupplyRun(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if last.Trigger != store.TriggerScheduled || last.Detail == "" {
		t.Fatalf("run=%+v", last)
	}
	if last.Unchecked == 0 {
		t.Fatal("the count of criteria nobody examined is part of what the pass did")
	}
}

// passRequest builds a real repository with real defects, so the scheduled
// pass is exercised against something it can actually find things in.
func passRequest(t *testing.T, ctx context.Context, db *store.Store, goalID string) PassRequest {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "T"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	files := map[string]string{
		"go.mod":           "module example.com/demo\n",
		"main.go":          "package main\n\nimport \"os\"\n\nfunc main() { _ = os.Getenv(\"REDIS_URL\") }\n",
		"web/package.json": `{"dependencies":{"react":"18.2.0"}}`,
		"web/index.html":   `<link href="https://fonts.googleapis.com/css2?family=Inter">`,
	}
	for name, body := range files {
		full := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(out))
	pack := standards.GoReactOfflineService()
	profile := standards.Profile{ProjectID: "PRJ-1", PackRef: pack.Ref(),
		Attributes: map[string]string{"frontend": "react", "network": "offline"}}
	if err = db.SaveStandardProfile(ctx, profile, pack); err != nil {
		t.Fatal(err)
	}
	return PassRequest{ProjectID: "PRJ-1", GoalID: goalID, Repository: repo, HeadSHA: head,
		ToolVersion: "test", Pack: pack, Profile: profile, Detectors: Default(),
		Schedule: DefaultSchedulePolicy(), Supply: DefaultSupplyPolicy()}
}
