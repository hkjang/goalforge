package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/verification"
)

// Registering a project and setting its goal are separate commands, so a
// project has no goal for as long as it takes somebody to run the second one.
// That is the ordinary state a new project is in, and it is where somebody
// trying the tool out stands.
//
// Every command built on activeGoal reported the store's bare "not found"
// there — naming neither what was missing nor what to run. Ten commands share
// this lookup, so the message belongs here rather than in each of them.
func TestAProjectWithNoGoalSaysSo(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository := t.TempDir()
	if err = db.CreateProject(ctx, model.Project{ID: "PRJ-1", Name: "p", RepositoryPath: repository,
		DefaultBranch: "main", Provider: "codex", Model: "sonnet", WIPLimit: 1}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repository)
	_, err = activeGoal(ctx, db)
	if err == nil {
		t.Fatal("this project has no goal")
	}
	if err.Error() == "not found" {
		t.Fatal("that names neither what is missing nor what to do about it")
	}
	// The remedy has to be in the message: the reader is one command away from
	// a working project and has no way to know which one.
	if !strings.Contains(err.Error(), "goal set") {
		t.Fatalf("the message must name the command: %v", err)
	}
	if !strings.Contains(err.Error(), "p") {
		t.Fatalf("and which project it is about: %v", err)
	}
}

// A project that has a goal returns it, and an unrelated failure is not
// relabelled as a missing goal.
func TestAProjectWithAGoalReturnsIt(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository := t.TempDir()
	if err = db.CreateProject(ctx, model.Project{ID: "PRJ-1", Name: "p", RepositoryPath: repository,
		DefaultBranch: "main", Provider: "codex", Model: "sonnet", WIPLimit: 1}); err != nil {
		t.Fatal(err)
	}
	set, err := db.SetGoal(ctx, "PRJ-1", "ship", "o", "",
		[]model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repository)
	goal, err := activeGoal(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if goal.ID != set.ID {
		t.Fatalf("goal=%+v", goal)
	}
}

// "integration verified" next to a failed completion gate reads as the goal
// being done. The required gates say the branch is not broken; the optional
// gates alongside them are the criteria that say whether the goal is finished,
// and a run that does not separate the two reports success for a goal with
// nothing implemented.
func TestTheIntegrationRunSaysWhichCriteriaAreUnmet(t *testing.T) {
	lines := integrationCriteriaLines([]verification.Result{
		{Type: "go_build", Status: "PASSED", Required: true},
		{Type: "redirect_behavior", Status: "FAILED"},
		{Type: "end_to_end_journey", Status: "FAILED"},
		{Type: "store_roundtrip", Status: "PASSED"},
	})
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"redirect_behavior", "end_to_end_journey", "2개"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the unmet criteria must be named (%q):\n%s", want, joined)
		}
	}
	// The required gate that passed is not a criterion and must not be listed
	// as unmet, nor must the optional one that passed.
	for _, unwanted := range []string{"go_build", "store_roundtrip"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("%s passed:\n%s", unwanted, joined)
		}
	}
	// And the reader has to be told the branch is still fine, or they will read
	// this as a broken main.
	if !strings.Contains(joined, "브랜치는 깨지지 않았") {
		t.Fatalf("a failed criterion is not a broken branch:\n%s", joined)
	}
}

// A failed required gate is not an unmet criterion. The caller returns before
// reaching here when one fails — a broken branch is reported as a broken branch
// — but this function is separate now and must not relabel one as a goal that
// is merely unfinished. The two call for opposite actions: fix main, or keep
// building.
func TestAFailedRequiredGateIsNotAnUnmetCriterion(t *testing.T) {
	lines := integrationCriteriaLines([]verification.Result{
		{Type: "go_build", Status: "FAILED", Required: true},
		{Type: "redirect_behavior", Status: "PASSED"},
	})
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "go_build") {
		t.Fatalf("a broken build is not an unfinished goal:\n%s", joined)
	}
	if !strings.Contains(joined, "모든 완료 조건") {
		t.Fatalf("every criterion here passed:\n%s", joined)
	}
}

// With every criterion met the run says so, because that is the sentence
// somebody is waiting for.
func TestTheIntegrationRunSaysWhenEveryCriterionIsMet(t *testing.T) {
	lines := integrationCriteriaLines([]verification.Result{
		{Type: "go_build", Status: "PASSED", Required: true},
		{Type: "redirect_behavior", Status: "PASSED"},
	})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "모든 완료 조건") {
		t.Fatalf("joined=%s", joined)
	}
	if strings.Contains(joined, "아직 충족되지 않은") {
		t.Fatalf("nothing is unmet:\n%s", joined)
	}
}
