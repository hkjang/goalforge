package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func TestCreateScoredIdeaAtomically(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/idea", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	g, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	score := model.IdeaScore{GoalContribution: 90, UserValue: 80, OperationalNeed: 70, Feasibility: 60, RiskReduction: 50, Difficulty: 40, PriorityScore: 75, Fingerprint: "abc", ExpectedChangeScope: "internal", ScopeExpansion: true, ApprovalRequired: true}
	idea, err := s.CreateScoredIdea(ctx, model.WorkItem{GoalID: g.ID, Title: "new scope"}, score)
	if err != nil {
		t.Fatal(err)
	}
	if idea.Status != "BLOCKED" || idea.Priority != 75 {
		t.Fatalf("idea=%+v", idea)
	}
	var approval int
	if err = s.db.QueryRow(`SELECT approval_required FROM idea_scores WHERE work_item_id=?`, idea.ID).Scan(&approval); err != nil {
		t.Fatal(err)
	}
	if approval != 1 {
		t.Fatalf("approval=%d", approval)
	}
}

func TestClaimNextWorkItemHonorsApprovalDependencyAndWIP(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/claim", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	g, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.CreateWorkItem(ctx, model.WorkItem{ID: "HIGH", GoalID: g.ID, Type: "IMPLEMENT", Title: "high", Priority: 100, Dependency: "DEP"})
	_, _ = s.CreateWorkItem(ctx, model.WorkItem{ID: "DEP", GoalID: g.ID, Type: "IMPLEMENT", Title: "dependency", Priority: 1})
	approved, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "APPROVED", GoalID: g.ID, Type: "IMPLEMENT", Title: "approved", Priority: 20, Status: "APPROVED"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimNextWorkItem(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != approved.ID || claimed.Status != "IN_PROGRESS" {
		t.Fatalf("claimed=%+v", claimed)
	}
	if _, err = s.ClaimNextWorkItem(ctx, g.ID); err == nil {
		t.Fatal("second WIP claim succeeded")
	}
}
