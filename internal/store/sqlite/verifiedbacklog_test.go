package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func verifiedFixture(t *testing.T) (*Store, model.Project, model.Goal, model.WorkItem) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := t.Context()
	project := model.Project{ID: "PRJ-1", Name: "p", RepositoryPath: "/r", DefaultBranch: "main",
		Provider: "codex", Model: "sonnet", WIPLimit: 1}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, project.ID, "ship", "o", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "t", Priority: 10, Weight: 1, ChangeScope: "x.go", Status: "APPROVED"})
	if err != nil {
		t.Fatal(err)
	}
	return s, project, goal, item
}

// A run that verifies has to leave its work item done, and the only thing that
// moves an item to IN_PROGRESS is the claim in ClaimNextWorkItem — which only
// the fresh-selection path calls. A resumed run takes its item from the
// checkpoint and never claims it, so every status update after that is a
// no-op against a guard that does not hold:
//
//	VERIFYING ... AND status='IN_PROGRESS'   -> 0 rows
//	DONE      ... AND status='VERIFYING'     -> 0 rows
//
// Nothing checked RowsAffected, so the item stayed exactly where the quota
// wait had put it: BACKLOG. Verification passed, the run checkpointed, and the
// goal could never complete because DoneWeight never reached TotalWeight.
func TestAVerifiedRunLeavesItsWorkItemDoneEvenWhenItWasNotClaimed(t *testing.T) {
	s, project, _, item := verifiedFixture(t)
	ctx := t.Context()
	// The state a resumed run starts from: the item is on the board because
	// the quota wait put it back, and nothing re-claimed it.
	if _, err := s.db.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRun(ctx, RunRecord{ID: "RUN-1", ProjectID: project.ID, WorkItemID: item.ID,
		Provider: "codex", Model: "sonnet", State: "RUNNING", TaskType: "CONTINUE_GOAL"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "RUN-1", "VERIFYING", "VERIFYING"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyVerificationOutcome(ctx, "RUN-1", true); err != nil {
		t.Fatal(err)
	}
	current, err := s.WorkItemByID(ctx, item.GoalID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "DONE" {
		t.Fatalf("the run verified, so the work is done: %s", current.Status)
	}
}

// The claimed path is unchanged: IN_PROGRESS -> VERIFYING -> DONE.
func TestAClaimedRunStillGoesThroughVerifying(t *testing.T) {
	s, project, goal, item := verifiedFixture(t)
	ctx := t.Context()
	claimed, err := s.ClaimNextWorkItem(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != item.ID || claimed.Status != "IN_PROGRESS" {
		t.Fatalf("claimed=%+v", claimed)
	}
	if err = s.StartRun(ctx, RunRecord{ID: "RUN-1", ProjectID: project.ID, WorkItemID: item.ID,
		Provider: "codex", Model: "sonnet", State: "RUNNING", TaskType: "CONTINUE_GOAL"}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, "RUN-1", "VERIFYING", "VERIFYING"); err != nil {
		t.Fatal(err)
	}
	mid, err := s.WorkItemByID(ctx, goal.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mid.Status != "VERIFYING" {
		t.Fatalf("mid=%s", mid.Status)
	}
	if _, err = s.ApplyVerificationOutcome(ctx, "RUN-1", true); err != nil {
		t.Fatal(err)
	}
	final, err := s.WorkItemByID(ctx, goal.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != "DONE" {
		t.Fatalf("final=%s", final.Status)
	}
}

// A failed run puts the item back on the board from wherever it was, for the
// same reason: the guard must not decide whether the outcome is recorded.
func TestAFailedRunPutsAnUnclaimedItemBackOnTheBoard(t *testing.T) {
	s, project, goal, item := verifiedFixture(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRun(ctx, RunRecord{ID: "RUN-1", ProjectID: project.ID, WorkItemID: item.ID,
		Provider: "codex", Model: "sonnet", State: "RUNNING", TaskType: "CONTINUE_GOAL"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "RUN-1", "VERIFYING", "VERIFYING"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyVerificationOutcome(ctx, "RUN-1", false); err != nil {
		t.Fatal(err)
	}
	current, err := s.WorkItemByID(ctx, goal.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "BACKLOG" {
		t.Fatalf("current=%s", current.Status)
	}
}

// Work somebody already finished is not re-finished, and work somebody
// discarded is not resurrected by a late verification — whichever way the
// verification went. A discarded item turned DONE by a passing run is work
// nobody wanted counted towards the goal.
func TestATerminalItemIsNotMovedByAVerification(t *testing.T) {
	for _, terminal := range []string{"DONE", "DISCARDED"} {
		for _, passed := range []bool{true, false} {
			s, project, goal, item := verifiedFixture(t)
			ctx := t.Context()
			if _, err := s.db.ExecContext(ctx, `UPDATE work_items SET status=? WHERE id=?`,
				terminal, item.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.StartRun(ctx, RunRecord{ID: "RUN-1", ProjectID: project.ID, WorkItemID: item.ID,
				Provider: "codex", Model: "sonnet", State: "RUNNING", TaskType: "CONTINUE_GOAL"}); err != nil {
				t.Fatal(err)
			}
			if err := s.FinishRun(ctx, "RUN-1", "VERIFYING", "VERIFYING"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ApplyVerificationOutcome(ctx, "RUN-1", passed); err != nil {
				t.Fatal(err)
			}
			current, err := s.WorkItemByID(ctx, goal.ID, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status != terminal {
				t.Fatalf("%s with passed=%v became %s", terminal, passed, current.Status)
			}
		}
	}
}

// The outcome is recorded whatever state the item is in, which is the contract
// StartRun's claim should make unnecessary and must not be relied on to.
//
// Something can still move the item while a run is being judged: a quota
// warning defers in-progress work to the board, a rollback puts it back, an
// unblock releases it. Any of those landing between the run finishing and its
// verification being applied used to leave the item exactly where it was —
// verification passed, the item sat in BACKLOG, and the goal's done weight
// never reached its total.
func TestTheOutcomeIsRecordedEvenIfSomethingMovedTheItemMeanwhile(t *testing.T) {
	s, project, goal, item := verifiedFixture(t)
	ctx := t.Context()
	if err := s.StartRun(ctx, RunRecord{ID: "RUN-1", ProjectID: project.ID, WorkItemID: item.ID,
		Provider: "codex", Model: "sonnet", State: "RUNNING", TaskType: "CONTINUE_GOAL"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "RUN-1", "VERIFYING", "VERIFYING"); err != nil {
		t.Fatal(err)
	}
	// Whatever moved it — this is the state the apply has to cope with.
	if _, err := s.db.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE id=?`,
		item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyVerificationOutcome(ctx, "RUN-1", true); err != nil {
		t.Fatal(err)
	}
	current, err := s.WorkItemByID(ctx, goal.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "DONE" {
		t.Fatalf("the run verified, so the work is done: %s", current.Status)
	}
}

// A run with a work item means that item is in progress. Enforced in StartRun
// because that is the one funnel every run goes through — the claim is reached
// only by the fresh-selection path, so a resumed run left its item on the
// board while working on it: the WIP limit did not count it and the next
// selection could pick the same item.
func TestStartingARunPutsItsWorkItemInProgress(t *testing.T) {
	s, project, goal, item := verifiedFixture(t)
	ctx := t.Context()
	for _, from := range []string{"BACKLOG", "APPROVED", "BLOCKED"} {
		if _, err := s.db.ExecContext(ctx, `UPDATE work_items SET status=? WHERE id=?`,
			from, item.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE projects SET state='READY' WHERE id=?`,
			project.ID); err != nil {
			t.Fatal(err)
		}
		runID := "RUN-" + from
		if err := s.StartRun(ctx, RunRecord{ID: runID, ProjectID: project.ID, WorkItemID: item.ID,
			Provider: "codex", Model: "sonnet", State: "RUNNING", TaskType: "CONTINUE_GOAL"}); err != nil {
			t.Fatalf("%s: %v", from, err)
		}
		current, err := s.WorkItemByID(ctx, goal.ID, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "IN_PROGRESS" {
			t.Fatalf("from %s: a run is working on it, so it is in progress, not %s", from, current.Status)
		}
	}
}

// Work somebody finished or discarded is not put back in progress by a run
// that mentions it. A late or duplicate run must not resurrect it.
func TestStartingARunDoesNotDisturbTerminalWork(t *testing.T) {
	for _, terminal := range []string{"DONE", "DISCARDED"} {
		s, project, goal, item := verifiedFixture(t)
		ctx := t.Context()
		if _, err := s.db.ExecContext(ctx, `UPDATE work_items SET status=? WHERE id=?`,
			terminal, item.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.StartRun(ctx, RunRecord{ID: "RUN-1", ProjectID: project.ID, WorkItemID: item.ID,
			Provider: "codex", Model: "sonnet", State: "RUNNING", TaskType: "CONTINUE_GOAL"}); err != nil {
			t.Fatal(err)
		}
		current, err := s.WorkItemByID(ctx, goal.ID, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != terminal {
			t.Fatalf("%s became %s", terminal, current.Status)
		}
	}
}

// An item already being verified stays verifying. A second run row for the
// same item must not walk it backwards while the first one's result is being
// judged.
func TestStartingARunDoesNotWalkAVerifyingItemBackwards(t *testing.T) {
	s, project, goal, item := verifiedFixture(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `UPDATE work_items SET status='VERIFYING' WHERE id=?`,
		item.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRun(ctx, RunRecord{ID: "RUN-2", ProjectID: project.ID, WorkItemID: item.ID,
		Provider: "codex", Model: "sonnet", State: "RUNNING", TaskType: "CONTINUE_GOAL"}); err != nil {
		t.Fatal(err)
	}
	current, err := s.WorkItemByID(ctx, goal.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "VERIFYING" {
		t.Fatalf("current=%s", current.Status)
	}
}
