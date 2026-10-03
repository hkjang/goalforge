package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func mergeFixture(t *testing.T) (*Store, model.Project, model.Goal) {
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
	goal, err := s.SetGoal(t.Context(), project.ID, "ship", "o", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	return s, project, goal
}

func verifiedItem(t *testing.T, s *Store, project model.Project, goal model.Goal, id, branch, sha string) {
	t.Helper()
	ctx := t.Context()
	if _, err := s.CreateWorkItem(ctx, model.WorkItem{ID: id, GoalID: goal.ID, Type: "IMPLEMENT",
		Title: id, Priority: 10, Weight: 1, ChangeScope: "x.go"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRunCommit(ctx, RunCommit{RunID: "RUN-" + id, ProjectID: project.ID,
		GoalID: goal.ID, WorkItemID: id, CommitSHA: sha, Branch: branch, FilesCommitted: 1,
		CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

// The gap the user hit. Each work item is implemented and verified in its own
// worktree, and nothing merges them: the operator had to merge by hand, so
// GoalForge could not reach "goal complete" on its own.
//
// Merging was possible — `goalforge merge --work-item ID` — but nothing said
// which items were waiting, so there was nothing to act on. A verified commit
// nobody is told about is the same as no commit.
func TestWorkWaitingToBeMergedIsReportable(t *testing.T) {
	s, project, goal := mergeFixture(t)
	ctx := t.Context()
	verifiedItem(t, s, project, goal, "W1", "goalforge/W1", "aaa111")
	verifiedItem(t, s, project, goal, "W2", "goalforge/W2", "bbb222")
	waiting, err := s.WorkWaitingToMerge(ctx, project.ID, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 2 {
		t.Fatalf("two verified commits are waiting: %+v", waiting)
	}
	for _, entry := range waiting {
		if entry.Branch == "" || entry.CommitSHA == "" {
			t.Fatalf("the branch and commit are what gets merged: %+v", entry)
		}
	}
}

// An item already merged is not waiting. The merge is recorded as an external
// effect keyed on the commit, so the answer comes from what was actually done
// rather than from the work item's status.
func TestAnAlreadyMergedItemIsNotWaiting(t *testing.T) {
	s, project, goal := mergeFixture(t)
	ctx := t.Context()
	verifiedItem(t, s, project, goal, "W1", "goalforge/W1", "aaa111")
	verifiedItem(t, s, project, goal, "W2", "goalforge/W2", "bbb222")
	key := EffectKey(EffectMergeBranch, project.ID, "W1", project.DefaultBranch, "aaa111")
	if _, _, err := s.BeginEffect(ctx, ExternalEffect{ProjectID: project.ID, RunID: "RUN-W1",
		WorkItemID: "W1", Kind: EffectMergeBranch, Target: project.DefaultBranch,
		Branch: "goalforge/W1", RequestHash: "aaa111", Key: key}); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleEffect(ctx, key, EffectSucceeded, "merged as ccc333"); err != nil {
		t.Fatal(err)
	}
	waiting, err := s.WorkWaitingToMerge(ctx, project.ID, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 || waiting[0].WorkItemID != "W2" {
		t.Fatalf("W1 is merged: %+v", waiting)
	}
}

// A merge that was attempted and failed is still waiting. Treating a recorded
// attempt as done would hide exactly the item somebody has to look at.
func TestAFailedMergeIsStillWaiting(t *testing.T) {
	s, project, goal := mergeFixture(t)
	ctx := t.Context()
	verifiedItem(t, s, project, goal, "W1", "goalforge/W1", "aaa111")
	key := EffectKey(EffectMergeBranch, project.ID, "W1", project.DefaultBranch, "aaa111")
	if _, _, err := s.BeginEffect(ctx, ExternalEffect{ProjectID: project.ID, RunID: "RUN-W1",
		WorkItemID: "W1", Kind: EffectMergeBranch, Target: project.DefaultBranch,
		Branch: "goalforge/W1", RequestHash: "aaa111", Key: key}); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleEffect(ctx, key, EffectFailed, "conflict in x.go"); err != nil {
		t.Fatal(err)
	}
	waiting, err := s.WorkWaitingToMerge(ctx, project.ID, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 {
		t.Fatalf("the merge did not happen, so it is still waiting: %+v", waiting)
	}
}

// An item with no verified commit is not waiting to be merged: there is nothing
// to merge, and listing it would send somebody to merge work that does not
// exist.
func TestAnItemWithNoCommitIsNotWaiting(t *testing.T) {
	s, project, goal := mergeFixture(t)
	ctx := t.Context()
	if _, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "t", Priority: 1, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	waiting, err := s.WorkWaitingToMerge(ctx, project.ID, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 0 {
		t.Fatalf("nothing was committed: %+v", waiting)
	}
}

// A discarded item is not waiting to be merged however verified its commit is.
// Somebody decided not to do this work, and listing it invites merging a change
// that was rejected on purpose.
func TestADiscardedItemIsNotWaiting(t *testing.T) {
	s, project, goal := mergeFixture(t)
	ctx := t.Context()
	verifiedItem(t, s, project, goal, "W1", "goalforge/W1", "aaa111")
	verifiedItem(t, s, project, goal, "W2", "goalforge/W2", "bbb222")
	current, err := s.WorkItemByID(ctx, goal.ID, "W1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyManualTransition(ctx, goal.ID, "W1", "DISCARDED", current.Version); err != nil {
		t.Fatal(err)
	}
	waiting, err := s.WorkWaitingToMerge(ctx, project.ID, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 || waiting[0].WorkItemID != "W2" {
		t.Fatalf("W1 was discarded: %+v", waiting)
	}
}
