package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

// The context package is what a session is handed before it touches anything.
// A decision whose code has moved must arrive marked, not as fact.
func TestStaleDecisionReachesTheSessionMarked(t *testing.T) {
	ctx := context.Background()
	root := gitRepo(t)
	base := head(t, root)
	s, err := Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: root, DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, project.ID, "G", "o", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordDecision(ctx, DesignDecision{ProjectID: project.ID, Title: "세션 저장소는 SQLite",
		Decision: "sqlite 를 쓴다", BaseCommit: base, Scope: "internal/session/**"}); err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT", Title: "작업"})
	if err != nil {
		t.Fatal(err)
	}
	// While nothing has moved, the decision is what it says it is.
	pkg, err := s.BuildContextPackage(ctx, project, goal, work)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Decisions) != 1 || pkg.Decisions[0].Standing != ContextDecided {
		t.Fatalf("a decision about untouched code stands: %+v", pkg.Decisions)
	}
	if pkg.Decisions[0].Caveat != "" {
		t.Fatalf("nothing to caveat: %q", pkg.Decisions[0].Caveat)
	}
	// Rewrite the code it was about.
	write(t, root, "internal/session/store.go", "package session\n\n// rewritten\n")
	commitAll(t, root, "rewrite")
	pkg, err = s.BuildContextPackage(ctx, project, goal, work)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Decisions[0].Standing != ContextAssumed {
		t.Fatalf("a decision whose code moved must not be handed over as settled: %+v", pkg.Decisions[0])
	}
	if !strings.Contains(pkg.Decisions[0].Caveat, "internal/session/store.go") {
		t.Fatalf("the session must be told what moved: %q", pkg.Decisions[0].Caveat)
	}
	// The decision is still included — it is the best record of why things are
	// as they are — just not presented as current.
	if !strings.Contains(pkg.Decisions[0].Body, "sqlite") {
		t.Fatalf("the reasoning must survive: %+v", pkg.Decisions[0])
	}
}

// A criterion with no evidence is not a measured fact however confidently its
// row reads, and the session must be told which is which.
func TestUnmeasuredCriteriaAreNotPresentedAsMeasured(t *testing.T) {
	ctx := context.Background()
	root := gitRepo(t)
	s, err := Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: root, DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, project.ID, "G", "o", "", []model.Criterion{
		{Type: "build_passed", ExpectedValue: "true"},
		{Type: "coverage", ExpectedValue: "85"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordVerification(ctx, goal.ID, "build_passed", "PASSED", "true", "ok"); err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT", Title: "작업"})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := s.BuildContextPackage(ctx, project, goal, work)
	if err != nil {
		t.Fatal(err)
	}
	standings := map[string]ContextItem{}
	for _, item := range pkg.Verification {
		if item.Kind == "criterion" {
			standings[item.Title] = item
		}
	}
	if standings["build_passed"].Standing != ContextMeasured {
		t.Fatalf("a passing gate is a measured fact: %+v", standings["build_passed"])
	}
	if standings["coverage"].Standing != ContextAssumed {
		t.Fatalf("a never-measured criterion is not a fact: %+v", standings["coverage"])
	}
	if standings["coverage"].Caveat == "" {
		t.Fatal("the session must be told why it is unconfirmed")
	}
}
