package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
	_, _ = s.CreateWorkItem(ctx, model.WorkItem{ID: "HIGH", GoalID: g.ID, Type: "IMPLEMENT", Title: "high", Priority: 100, Dependencies: []string{"DEP"}})
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

// Raising the WIP limit only helps if a busy area of the tree does not block
// the rest of the backlog: the claim skips candidates whose scope overlaps
// work in progress instead of stopping at the first one.
func TestClaimSkipsScopeConflictsInsteadOfStopping(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex", WIPLimit: 2}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	g, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []model.WorkItem{
		{ID: "API-A", GoalID: g.ID, Type: "IMPLEMENT", Title: "api a", Priority: 100, ChangeScope: "internal/api/**"},
		{ID: "API-B", GoalID: g.ID, Type: "IMPLEMENT", Title: "api b", Priority: 90, ChangeScope: "internal/api/**"},
		{ID: "STORE-C", GoalID: g.ID, Type: "IMPLEMENT", Title: "store c", Priority: 80, ChangeScope: "internal/store/**"},
	} {
		if _, err = s.CreateWorkItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ClaimNextWorkItem(ctx, g.ID)
	if err != nil || first.ID != "API-A" {
		t.Fatalf("highest priority first: %+v err=%v", first, err)
	}
	second, err := s.ClaimNextWorkItem(ctx, g.ID)
	if err != nil || second.ID != "STORE-C" {
		t.Fatalf("the conflicting candidate must be skipped, not refused: %+v err=%v", second, err)
	}
	// With both slots busy the limit, not the scope, is what stops the third.
	if _, err = s.ClaimNextWorkItem(ctx, g.ID); err == nil || !strings.Contains(err.Error(), "WIP limit") {
		t.Fatalf("WIP limit: %v", err)
	}
}

// When every claimable item overlaps something running, that is a transient
// state a worker should wait out, not the end of the backlog.
func TestClaimReportsConflictSeparatelyFromEmptyBacklog(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex", WIPLimit: 3}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	g, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNextWorkItem(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an empty backlog is ErrNotFound: %v", err)
	}
	for _, item := range []model.WorkItem{
		{ID: "A", GoalID: g.ID, Type: "IMPLEMENT", Title: "a", Priority: 100, ChangeScope: "internal/api/**"},
		{ID: "B", GoalID: g.ID, Type: "IMPLEMENT", Title: "b", Priority: 90, ChangeScope: "internal/api/handlers/**"},
	} {
		if _, err = s.CreateWorkItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.ClaimNextWorkItem(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNextWorkItem(ctx, g.ID); !errors.Is(err, ErrAllCandidatesConflict) {
		t.Fatalf("all candidates conflicting is its own condition: %v", err)
	}
}

// A person holding one item does not stop automation from working elsewhere,
// but it does keep agents out of that person's files.
func TestHumanHeldWorkDoesNotConsumeTheAgentWIPSlot(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	g, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []model.WorkItem{
		{ID: "HELD", GoalID: g.ID, Type: "IMPLEMENT", Title: "held", Priority: 100, ChangeScope: "internal/api/**"},
		{ID: "OTHER", GoalID: g.ID, Type: "IMPLEMENT", Title: "other", Priority: 90, ChangeScope: "internal/store/**"},
		{ID: "SAME-AREA", GoalID: g.ID, Type: "IMPLEMENT", Title: "same area", Priority: 95, ChangeScope: "internal/api/**"},
	} {
		if _, err = s.CreateWorkItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.TakeOverWorkItem(ctx, p.ID, g.ID, "HELD", "직접 고친다", "/tmp/ws"); err != nil {
		t.Fatal(err)
	}
	// The default WIP limit is one, and the human item must not occupy it.
	claimed, err := s.ClaimNextWorkItem(ctx, g.ID)
	if err != nil {
		t.Fatalf("automation must keep working elsewhere: %v", err)
	}
	if claimed.ID != "OTHER" {
		t.Fatalf("agents must stay out of the files a person is editing, got %s", claimed.ID)
	}
}
