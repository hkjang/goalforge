package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func blockedFixture(t *testing.T) (*Store, model.Project) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	project := model.Project{ID: "PRJ-1", Name: "p", RepositoryPath: "/r", DefaultBranch: "main",
		Provider: "codex", Model: "sonnet", WIPLimit: 1}
	if err = s.CreateProject(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	return s, project
}

// A project blocked by a policy violation had no way back.
//
// The only transition out of BLOCKED was the checkpoint resume, and a run
// stopped by a policy violation never checkpointed — so the resume failed
// looking for one and the project stayed blocked forever, even after the thing
// that caused the violation was fixed. There was no unblock anywhere.
func TestABlockedProjectCanBeUnblockedWithAReason(t *testing.T) {
	s, project := blockedFixture(t)
	ctx := t.Context()
	if err := s.BlockProject(ctx, project.ID, "work item changed files outside declared scope"); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.ProjectByID(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.State != "BLOCKED" {
		t.Fatalf("state=%s", blocked.State)
	}
	if err = s.UnblockProject(ctx, project.ID, "변경 범위를 경로 목록으로 고쳤습니다"); err != nil {
		t.Fatal(err)
	}
	ready, err := s.ProjectByID(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != "READY" {
		t.Fatalf("the project must be runnable again: %s", ready.State)
	}
}

// Unblocking is a deliberate act and the reason is recorded. A state change
// nobody has to justify is one that gets made to quiet an error rather than to
// fix it, and the next reader cannot tell which happened.
func TestUnblockingNeedsAReason(t *testing.T) {
	s, project := blockedFixture(t)
	ctx := t.Context()
	if err := s.BlockProject(ctx, project.ID, "x"); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"", "   "} {
		if err := s.UnblockProject(ctx, project.ID, reason); err == nil {
			t.Fatalf("a reason is required: %q", reason)
		}
	}
	blocked, err := s.ProjectByID(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.State != "BLOCKED" {
		t.Fatalf("a refused unblock must not have moved it: %s", blocked.State)
	}
}

// A project that is not blocked is not unblocked, and the refusal says what
// state it is actually in. "project state is not BLOCKED" leaves the operator
// to go and look; naming the state answers the question they were about to ask.
func TestUnblockingARunnableProjectIsRefused(t *testing.T) {
	s, project := blockedFixture(t)
	err := s.UnblockProject(t.Context(), project.ID, "이유")
	if err == nil {
		t.Fatal("this project is not blocked")
	}
	current, projErr := s.ProjectByID(t.Context(), project.ID)
	if projErr != nil {
		t.Fatal(projErr)
	}
	if !contains(err.Error(), current.State) {
		t.Fatalf("the refusal must name the state it found (%s): %v", current.State, err)
	}
}

// Replacing an unusable scope with another unusable one leaves the item exactly
// as stuck, having told the operator it was fixed.
func TestSettingAScopeRefusesOneThatIsNotAPathList(t *testing.T) {
	s, project := blockedFixture(t)
	ctx := t.Context()
	goal, err := s.SetGoal(ctx, project.ID, "ship", "o", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "t", Priority: 1, ChangeScope: "handler.go 신설: 설명"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetWorkItemScope(ctx, goal.ID, "W1", "main.go 를 교체"); err == nil {
		t.Fatal("a sentence is not a path list, however it arrives")
	}
	unchanged, err := s.WorkItemByID(ctx, goal.ID, "W1")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.ChangeScope != "handler.go 신설: 설명" {
		t.Fatalf("a refused change must not have been written: %q", unchanged.ChangeScope)
	}
	// And a real path list is accepted.
	updated, err := s.SetWorkItemScope(ctx, goal.ID, "W1", "handler.go,internal/server/**")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ChangeScope != "handler.go,internal/server/**" {
		t.Fatalf("scope=%q", updated.ChangeScope)
	}
	// An item that is not there is not silently created.
	if _, err = s.SetWorkItemScope(ctx, goal.ID, "W-404", "handler.go"); err == nil {
		t.Fatal("there is no such work item")
	}
}

// The reason survives, because the question asked later is why somebody
// decided the block could be cleared.
func TestTheUnblockReasonIsRecorded(t *testing.T) {
	s, project := blockedFixture(t)
	ctx := t.Context()
	if err := s.BlockProject(ctx, project.ID, "범위 위반"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnblockProject(ctx, project.ID, "범위를 store.go 로 고쳤습니다"); err != nil {
		t.Fatal(err)
	}
	events, err := s.ProjectStateHistory(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, event := range events {
		if event.From == "BLOCKED" && event.To == "READY" {
			found = true
			if !contains(event.Reason, "store.go") {
				t.Fatalf("the reason must survive: %+v", event)
			}
		}
	}
	if !found {
		t.Fatalf("the transition must be recorded: %+v", events)
	}
}

// A run stopped part-way leaves its work item IN_PROGRESS, and nothing moves
// it back: ApplyManualTransition refuses to move an item out of an engine
// phase, because the run is supposed to still be going. After a block the run
// is not going, and the item holds the WIP slot forever — so the next run is
// refused with "implementation WIP limit reached" and the project cannot
// progress even once the block is cleared.
//
// The quota path already returns the item (EnterQuotaWait does it); the
// blocking paths did not.
func TestUnblockingReturnsAnOrphanedItemToTheBoard(t *testing.T) {
	s, project := blockedFixture(t)
	ctx := t.Context()
	goal, err := s.SetGoal(ctx, project.ID, "ship", "o", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "t", Priority: 1, Status: "IN_PROGRESS"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "W2", GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "u", Priority: 1, Status: "DONE"}); err != nil {
		t.Fatal(err)
	}
	if err = s.BlockProject(ctx, project.ID, "범위 위반"); err != nil {
		t.Fatal(err)
	}
	if err = s.UnblockProject(ctx, project.ID, "범위를 고쳤습니다"); err != nil {
		t.Fatal(err)
	}
	orphan, err := s.WorkItemByID(ctx, goal.ID, "W1")
	if err != nil {
		t.Fatal(err)
	}
	if orphan.Status != "BACKLOG" {
		t.Fatalf("an item left in flight by a stopped run goes back on the board: %s", orphan.Status)
	}
	// Finished work is not disturbed. Returning everything would undo work
	// that was done.
	done, err := s.WorkItemByID(ctx, goal.ID, "W2")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "DONE" {
		t.Fatalf("finished work stays finished: %s", done.Status)
	}
}
