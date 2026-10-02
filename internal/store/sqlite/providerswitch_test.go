package sqlite

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func switchFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.CreateProject(t.Context(), model.Project{ID: "PRJ-1", Name: "p", RepositoryPath: "/r",
		DefaultBranch: "main", Provider: "codex", Model: "sonnet", WIPLimit: 1}); err != nil {
		t.Fatal(err)
	}
	return s, "PRJ-1"
}

// Registering a project and setting its goal are separate commands, so a
// project sits with no goal for as long as it takes somebody to run the second
// one — and choosing the provider is a natural thing to do first.
//
// It was refused, because the handoff bundles the goal for the incoming
// provider and the lookup failed with a bare "not found". Nothing is being
// handed off when no work has started: there is no goal to carry and no run to
// resume, so the switch is just a setting.
func TestTheProviderCanBeChosenBeforeThereIsAGoal(t *testing.T) {
	s, projectID := switchFixture(t)
	ctx := t.Context()
	handoff, err := s.SwitchProvider(ctx, projectID, "claude", "sonnet", "제공자 선택")
	if err != nil {
		t.Fatalf("there is nothing to hand off yet, which is not a failure: %v", err)
	}
	if handoff.ToProvider != "claude" {
		t.Fatalf("handoff=%+v", handoff)
	}
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if project.Provider != "claude" || project.Model != "sonnet" {
		t.Fatalf("the switch must take effect: %s/%s", project.Provider, project.Model)
	}
}

// With a goal, the handoff still carries it. The incoming provider has to be
// told where things stood, and a switch that dropped that would hand over a
// project with no context at the moment context matters most.
func TestASwitchWithAGoalStillCarriesIt(t *testing.T) {
	s, projectID := switchFixture(t)
	ctx := t.Context()
	goal, err := s.SetGoal(ctx, projectID, "ship", "objective", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "t", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	handoff, err := s.SwitchProvider(ctx, projectID, "claude", "sonnet", "제공자 전환")
	if err != nil {
		t.Fatal(err)
	}
	if handoff.GoalVersion != goal.Version {
		t.Fatalf("the handoff must pin the goal version it carried: %+v", handoff)
	}
	for _, want := range []string{"ship", "W1"} {
		if !contains(handoff.ContentJSON, want) {
			t.Fatalf("the handoff must carry %q: %s", want, handoff.ContentJSON)
		}
	}
}

// A real failure reading the goal is still a failure. Treating every error as
// "no goal yet" would hand a project over while its state could not be read —
// and the handoff would carry an empty goal that the incoming provider reads
// as "nothing was in progress".
//
// The project has to stay readable while the goal does not, or the switch
// fails earlier for a different reason and this proves nothing. Closing the
// database did exactly that, and the test passed without reaching the branch
// it was written for.
func TestAnUnreadableGoalIsNotTreatedAsAbsent(t *testing.T) {
	s, projectID := switchFixture(t)
	ctx := t.Context()
	if _, err := s.SetGoal(ctx, projectID, "ship", "o", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE goals`); err != nil {
		t.Fatal(err)
	}
	_, err := s.SwitchProvider(ctx, projectID, "claude", "sonnet", "r")
	if err == nil {
		t.Fatal("the goal could not be read, so the state being handed over is unknown")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("an unreadable goal is not an absent one: %v", err)
	}
	// And nothing was switched: a project whose state could not be read must
	// not be handed to another provider.
	project, projErr := s.ProjectByID(ctx, projectID)
	if projErr != nil {
		t.Fatal(projErr)
	}
	if project.Provider != "codex" {
		t.Fatalf("the switch must not have taken effect: %s", project.Provider)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
