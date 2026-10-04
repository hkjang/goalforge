package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func baseFixture(t *testing.T) (*Store, model.Project, model.Goal) {
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
	return s, project, goal
}

func commitFor(t *testing.T, s *Store, p model.Project, g model.Goal, item, sha, branch string, at time.Time) {
	t.Helper()
	if _, err := s.CreateWorkItem(t.Context(), model.WorkItem{ID: item, GoalID: g.ID,
		Type: "IMPLEMENT", Title: item, Priority: 1, Weight: 1, ChangeScope: "x.go"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRunCommit(t.Context(), RunCommit{RunID: "RUN-" + item, ProjectID: p.ID,
		GoalID: g.ID, WorkItemID: item, CommitSHA: sha, Branch: branch, FilesCommitted: 1,
		CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
}

// Every worktree was cut from the default branch, so a work item could not see
// what the previous items had built. Item 1 wrote store/, verified, and was
// marked done — and item 3's worktree, cut from a main that nothing had been
// merged into, failed with:
//
//	stat …/IDEA-…/store: directory not found
//
// So every item that depends on earlier work failed, and the chain could not
// build a program made of more than one piece.
//
// The base for new work is the tip of the last verified work instead, so the
// work accumulates. The approval boundary does not move: that is about the
// default branch, which still nothing is merged into without one.
func TestTheBaseForNewWorkIsTheLastVerifiedCommit(t *testing.T) {
	s, project, goal := baseFixture(t)
	ctx := t.Context()
	start := time.Now().UTC().Add(-time.Hour)
	commitFor(t, s, project, goal, "W1", "aaa111", "goalforge/W1", start)
	commitFor(t, s, project, goal, "W2", "bbb222", "goalforge/W2", start.Add(time.Minute))
	base, err := s.LatestGoalCommit(ctx, project.ID, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if base.CommitSHA != "bbb222" {
		t.Fatalf("the newest verified work is the base: %+v", base)
	}
	if base.WorkItemID != "W2" {
		t.Fatalf("base=%+v", base)
	}
}

// With nothing built yet there is no base to inherit, and the caller falls back
// to the default branch. Reported as not found rather than as an empty commit,
// because an empty SHA handed to `git worktree add` is a different failure with
// a worse message.
func TestWithNoVerifiedWorkThereIsNoBase(t *testing.T) {
	s, project, goal := baseFixture(t)
	if _, err := s.LatestGoalCommit(t.Context(), project.ID, goal.ID); err == nil {
		t.Fatal("nothing has been built for this goal")
	}
}

// A commit from another goal is not a base for this one. Goals are separate
// pieces of work and inheriting across them would put one goal's changes into
// the other's worktree.
func TestACommitFromAnotherGoalIsNotABase(t *testing.T) {
	s, project, goal := baseFixture(t)
	ctx := t.Context()
	commitFor(t, s, project, goal, "W1", "aaa111", "goalforge/W1", time.Now().UTC())
	// A second goal supersedes the first; its work starts from the branch.
	second, err := s.SetGoal(ctx, project.ID, "next", "o2", "변경",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.LatestGoalCommit(ctx, project.ID, second.ID); err == nil {
		t.Fatal("the new goal has built nothing")
	}
	// And the first goal still reports its own.
	base, err := s.LatestGoalCommit(ctx, project.ID, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if base.CommitSHA != "aaa111" {
		t.Fatalf("base=%+v", base)
	}
}

// Work that was discarded is not a base. Somebody decided not to do it, and
// building the next item on top would carry it in anyway.
func TestDiscardedWorkIsNotABase(t *testing.T) {
	s, project, goal := baseFixture(t)
	ctx := t.Context()
	start := time.Now().UTC().Add(-time.Hour)
	commitFor(t, s, project, goal, "W1", "aaa111", "goalforge/W1", start)
	commitFor(t, s, project, goal, "W2", "bbb222", "goalforge/W2", start.Add(time.Minute))
	current, err := s.WorkItemByID(ctx, goal.ID, "W2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyManualTransition(ctx, goal.ID, "W2", "DISCARDED", current.Version); err != nil {
		t.Fatal(err)
	}
	base, err := s.LatestGoalCommit(ctx, project.ID, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if base.CommitSHA != "aaa111" {
		t.Fatalf("W2 was discarded, so W1 is the base: %+v", base)
	}
}
