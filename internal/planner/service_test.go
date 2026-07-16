package planner

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func TestServicePersistsAndSelectsHighestIdea(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/service", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	g, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(s, DefaultPolicy())
	low := candidate("낮은 가치 개선")
	low.GoalContribution = 40
	low.UserValue = 40
	high := candidate("높은 가치 개선")
	result, err := service.DiscoverAndStore(ctx, g.ID, []Candidate{low, high})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted) != 2 {
		t.Fatalf("result=%+v", result)
	}
	selected, err := service.SelectNext(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Title != "높은 가치 개선" || selected.Status != "IN_PROGRESS" {
		t.Fatalf("selected=%+v", selected)
	}
}
