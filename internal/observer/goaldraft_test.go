package observer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/rrsi"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/verification"
)

func criterion(name, expected, kind string, command ...string) DraftCriterion {
	return DraftCriterion{Type: name, ExpectedValue: expected, Kind: kind,
		GateCommand: command, WhyItFailsNow: "아직 구현하지 않았기 때문입니다"}
}

// runnerCriterion is a filtered test run with the assertion that the named test
// ran. Without it the gate passes on a package with no test file, which the
// screen now refuses — a fixture that left it out was itself the vacuous gate.
func runnerCriterion(name, test string, command ...string) DraftCriterion {
	entry := criterion(name, test, "test", command...)
	entry.ValuePattern = `--- PASS: (` + test + `)`
	return entry
}

// goRepo is a repository the fixture's health gate passes in. A health gate
// has to pass now, so the tests that run gates need somewhere it can.
func goRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func draftOf(criteria ...DraftCriterion) GoalDraft {
	// A health gate, because a draft without one is refused on its own account
	// and these tests are about the criteria. Partial code has to pass it, so
	// it is a build check rather than a completion criterion.
	health := criterion("module_present", "true", "build", "test", "-f", "go.mod")
	return GoalDraft{Title: "메모 저장", Objective: "사용자가 메모를 저장할 수 있다",
		Criteria: criteria, Health: &health}
}

// A gate built from a command that always succeeds is green from the moment it
// is written and stays green through every change — the most expensive way to
// have no gate at all.
func TestAGateThatCannotFailIsRefused(t *testing.T) {
	for _, command := range [][]string{{"true"}, {"echo", "ok"}, {"/bin/true"}, {":"}} {
		refusals := ScreenDraft(draftOf(criterion("note_saves", "true", "journey", command...)))
		if len(refusals) == 0 {
			t.Fatalf("%v must be refused", command)
		}
		found := false
		for _, refusal := range refusals {
			if refusal.Kind == RefusalAlwaysPasses {
				found = true
			}
		}
		if !found {
			t.Fatalf("%v: refusals=%+v", command, refusals)
		}
	}
	// A real command is not refused for its name.
	if refusals := ScreenDraft(draftOf(criterion("note_saves", "true", "journey",
		"./scripts/journey.sh", "notes"))); len(refusals) != 0 {
		t.Fatalf("refusals=%s", rrsi.Explain(refusals))
	}
}

// The rule that makes a proposed gate mean something. A gate for work not yet
// done that passes today will pass tomorrow, and the criterion it settles is
// satisfied before anybody writes anything.
func TestAGateThatAlreadyPassesIsRefused(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine, err := verification.New(db, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository := goRepo(t)
	// A gate that checks for a file nobody has written fails, as it should.
	missing := draftOf(criterion("note_saves", "true", "journey", "test", "-f", "notes.go"))
	refusals := VerifyDraftFailsNow(context.Background(), engine, repository, &missing)
	if len(refusals) != 0 {
		t.Fatalf("a gate for unwritten work must fail now: %s", rrsi.Explain(refusals))
	}
	if !missing.Criteria[0].FailsNow {
		t.Fatal("and that must be recorded")
	}
	// A gate that checks for something already there passes, and that is the
	// problem: the goal would be complete at the moment it was created.
	if err = os.WriteFile(filepath.Join(repository, "notes.go"), []byte("package notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	present := draftOf(criterion("note_saves", "true", "journey", "test", "-f", "notes.go"))
	refusals = VerifyDraftFailsNow(context.Background(), engine, repository, &present)
	if len(refusals) == 0 {
		t.Fatal("a gate that already passes is not measuring the work")
	}
	if refusals[0].Kind != RefusalPassesAlready {
		t.Fatalf("refusals=%+v", refusals)
	}
	// The refusal quotes the claim, so a reader can see what the draft said
	// about why it would fail and compare it against what happened.
	if !strings.Contains(refusals[0].Detail, "아직 구현하지 않았기 때문입니다") {
		t.Fatalf("detail=%q", refusals[0].Detail)
	}
}

// A command that does not exist proves nothing about whether the work is done.
// Counting it as a failure would let a typo stand in for a measurement.
func TestAGateThatCannotBeRunIsNotCountedAsFailing(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine, err := verification.New(db, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	draft := draftOf(criterion("note_saves", "true", "journey", "./no-such-script-at-all"))
	refusals := VerifyDraftFailsNow(context.Background(), engine, goRepo(t), &draft)
	if len(refusals) == 0 {
		t.Fatal("a gate that cannot run has not shown the work is undone")
	}
}

// "잘 동작함" is a sentence, not a threshold. A criterion whose expected value
// cannot be compared against a measurement is settled by argument.
func TestAnExpectedValueMustBeComparable(t *testing.T) {
	refusals := ScreenDraft(draftOf(criterion("note_saves", "메모가 잘 저장되면 통과", "journey", "./s.sh")))
	found := false
	for _, refusal := range refusals {
		if refusal.Kind == RefusalUnjudgeable {
			found = true
		}
	}
	if !found {
		t.Fatalf("refusals=%+v", refusals)
	}
	// Directions and units are comparable, and so is a bare boolean.
	for _, value := range []string{"true", "<=200ms", ">=99%", "=0", "false"} {
		if refusals = ScreenDraft(draftOf(criterion("x_check", value, "journey", "./s.sh"))); len(refusals) != 0 {
			t.Fatalf("%q: %s", value, rrsi.Explain(refusals))
		}
	}
}

// A criterion with no way to settle it is a sentence somebody will argue about
// later, so the gate is asked for with it rather than separately.
func TestACriterionMustArriveWithItsGate(t *testing.T) {
	refusals := ScreenDraft(draftOf(DraftCriterion{Type: "note_saves", ExpectedValue: "true",
		Kind: "journey", WhyItFailsNow: "아직입니다"}))
	if len(refusals) == 0 {
		t.Fatal("a criterion with no gate command must be refused")
	}
	// And the kind has to be one that can settle it.
	if refusals = ScreenDraft(draftOf(criterion("note_saves", "true", "vibes", "./s.sh"))); len(refusals) == 0 {
		t.Fatal("an invented check kind must be refused")
	}
}

// A draft with nothing to judge is not a goal.
func TestADraftWithNoCriteriaIsRefused(t *testing.T) {
	refusals := ScreenDraft(GoalDraft{Title: "t", Objective: "o"})
	if len(refusals) == 0 {
		t.Fatal("a goal nothing can judge complete is not one")
	}
	if !strings.Contains(rrsi.Explain(refusals), "판정") {
		t.Fatalf("refusals=%+v", refusals)
	}
	// Two criteria with one name cannot both be settled.
	duplicate := draftOf(criterion("note_saves", "true", "journey", "./a.sh"),
		criterion("note_saves", "true", "journey", "./b.sh"))
	if refusals = ScreenDraft(duplicate); len(refusals) == 0 {
		t.Fatal("the same criterion twice must be refused")
	}
}
