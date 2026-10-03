package observer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/rrsi"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/verification"
)

// A goal's completion criteria and a run's health gates are different things,
// and the rest of the system keeps them apart: verification gates run after
// every work item and decide whether that run broke anything, while goal
// criteria decide whether the goal is done.
//
// The draft installed every completion criterion as a *required* gate. A goal
// with six feature criteria then had six gates that no single work item could
// make pass — the first item implements the POST handler and the end-to-end
// journey gate still fails because nothing else exists yet. Every run failed
// verification, the repair loop burned its attempts, and the project ended
// BLOCKED. A multi-part goal could never make progress.
//
// Measured by this: a completion criterion is not a required gate.
func TestCompletionCriteriaAreNotRequiredGates(t *testing.T) {
	draft := GoalDraft{Title: "t", Objective: "o",
		Criteria: []DraftCriterion{
			{Type: "redirect_roundtrip", ExpectedValue: "TestRedirect", Kind: "journey",
				GateCommand:  []string{"go", "test", "-v", "-run", "^TestRedirect$", "./..."},
				ValuePattern: `--- PASS: (TestRedirect)`, FailsNow: true},
			{Type: "binary_journey", ExpectedValue: "TestJourney", Kind: "journey",
				GateCommand:  []string{"go", "test", "-v", "-run", "^TestJourney$", "./e2e"},
				ValuePattern: `--- PASS: (TestJourney)`, FailsNow: true},
		},
		Health: &DraftCriterion{Type: "build_passed", ExpectedValue: "true", Kind: "build",
			GateCommand: []string{"go", "build", "./..."}, FailsNow: false},
	}
	gates := draft.Gates()
	if len(gates) != 3 {
		t.Fatalf("two completion gates and one health gate: %+v", gates)
	}
	required, optional := 0, 0
	for _, gate := range gates {
		if gate.Required {
			required++
			if gate.Type != "build_passed" {
				t.Fatalf("only the health gate may fail a run: %+v", gate)
			}
		} else {
			optional++
		}
	}
	if required != 1 || optional != 2 {
		t.Fatalf("required=%d optional=%d", required, optional)
	}
}

// A draft with no health gate cannot be applied: the runner refuses a project
// whose gates are all optional, so the goal would be set and nothing could run.
// That is worse than refusing the draft, because the refusal says what is
// missing and the other leaves the operator with a project that looks ready.
func TestADraftWithNoHealthGateIsRefused(t *testing.T) {
	draft := GoalDraft{Title: "t", Objective: "o",
		Criteria: []DraftCriterion{{Type: "redirect_roundtrip", ExpectedValue: "true", Kind: "test",
			GateCommand: []string{"go", "test", "-run", "TestRedirect"}, FailsNow: true}}}
	refusals := ScreenDraft(draft)
	var found bool
	for _, refusal := range refusals {
		if refusal.Kind == RefusalNoHealthGate {
			found = true
		}
	}
	if !found {
		t.Fatalf("a draft whose gates are all optional cannot run: %+v", refusals)
	}
}

// ScreenDraft is static. Whether a gate passes is answered by running it, and
// that answer belongs to VerifyDraftFailsNow — the screen asking it too would
// be the same rule in two places, with the screen reading a field nothing has
// filled in yet.
func TestTheScreenDoesNotAnswerWhetherGatesPass(t *testing.T) {
	draft := GoalDraft{Title: "t", Objective: "o",
		Criteria: []DraftCriterion{{Type: "redirect_roundtrip", ExpectedValue: "TestRedirect", Kind: "journey",
			GateCommand:   []string{"go", "test", "-v", "-run", "^TestRedirect$", "./..."},
			ValuePattern:  `--- PASS: (TestRedirect)`,
			WhyItFailsNow: "리다이렉트 핸들러가 아직 없습니다"}},
		// Neither FailsNow is set, because nothing has run yet. That is the
		// state the screen sees.
		Health: &DraftCriterion{Type: "build_passed", ExpectedValue: "true", Kind: "build",
			GateCommand: []string{"go", "build", "./..."}}}
	refusals := ScreenDraft(draft)
	for _, refusal := range refusals {
		switch refusal.Kind {
		case RefusalPassesAlready, RefusalHealthGateFails:
			t.Fatalf("the screen has not run anything: %+v", refusal)
		}
	}
	if len(refusals) != 0 {
		t.Fatalf("this draft is structurally complete: %+v", refusals)
	}
}

// The health gate is run too, and the answer wanted is the opposite one. A
// proposer that says "go build ./... passes" about a repository that does not
// compile would install a gate failing every run — so it is checked rather
// than trusted.
func TestAHealthGateThatFailsNowIsRefusedByTheVerifier(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine, err := verification.New(db, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir() // no go.mod, so the health gate below fails
	draft := draftOf(criterion("note_saves", "true", "journey", "test", "-f", "notes.go"))
	refusals := VerifyDraftFailsNow(context.Background(), engine, repository, &draft)
	var found bool
	for _, refusal := range refusals {
		if refusal.Kind == RefusalHealthGateFails {
			found = true
		}
	}
	if !found {
		t.Fatalf("a health gate that fails today fails every run: %s", rrsi.Explain(refusals))
	}
	if !draft.Health.FailsNow {
		t.Fatal("and that has to be recorded, or the screen cannot see it")
	}
}

// A health gate that passes is accepted, and the completion criterion that
// fails is accepted alongside it. The two checks are opposite and both run.
func TestAHealthGateThatPassesIsAccepted(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine, err := verification.New(db, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	draft := draftOf(criterion("note_saves", "true", "journey", "test", "-f", "notes.go"))
	refusals := VerifyDraftFailsNow(context.Background(), engine, goRepo(t), &draft)
	if len(refusals) != 0 {
		t.Fatalf("the criterion fails and the health gate passes: %s", rrsi.Explain(refusals))
	}
	if draft.Health.FailsNow {
		t.Fatal("the health gate passed")
	}
	if !draft.Criteria[0].FailsNow {
		t.Fatal("the criterion failed")
	}
}

// A health gate built from a command that always succeeds is refused for the
// same reason a criterion is: it is the one required gate, and one that cannot
// fail means every run is verified by something green before it ran.
func TestATrivialHealthGateIsRefused(t *testing.T) {
	health := criterion("always", "true", "build", "true")
	draft := draftOf(criterion("note_saves", "true", "journey", "./a.sh"))
	draft.Health = &health
	refusals := ScreenDraft(draft)
	var found bool
	for _, refusal := range refusals {
		if refusal.Kind == RefusalNoHealthGate {
			found = true
		}
	}
	if !found {
		t.Fatalf("a required gate that cannot fail catches nothing: %+v", refusals)
	}
}

// Every criterion a unit test settles was written by the same hand as the code
// it tests. A draft with nothing that runs the product has only that, so it is
// refused rather than set as a goal that can complete without the program ever
// having been run on input it was not written against.
func TestADraftWithoutAJourneyGateIsRefused(t *testing.T) {
	unit := func(name, test string) DraftCriterion {
		return DraftCriterion{Type: name, ExpectedValue: test, Kind: "test",
			GateCommand:   []string{"go", "test", "-count=1", "-v", "-run", "^" + test + "$", "./..."},
			ValuePattern:  "--- PASS: (" + test + ")",
			WhyItFailsNow: "아직 구현하지 않았습니다"}
	}
	health := &DraftCriterion{Type: "build_passed", ExpectedValue: "true", Kind: "build",
		GateCommand: []string{"go", "build", "./..."}}
	draft := GoalDraft{Title: "t", Objective: "o", Health: health,
		Criteria: []DraftCriterion{unit("parse", "TestParse"), unit("render", "TestRender")}}

	found := false
	for _, refusal := range ScreenDraft(draft) {
		if refusal.Kind == RefusalNoJourney {
			found = true
		}
	}
	if !found {
		t.Fatal("a draft settled only by unit tests must be refused")
	}

	draft.Criteria = append(draft.Criteria, DraftCriterion{Type: "cli_on_sample", ExpectedValue: "true", Kind: "journey",
		GateCommand:   []string{"go", "run", "./cmd/tool", "testdata/sample.log"},
		ValuePattern:  "total requests",
		WhyItFailsNow: "cmd/tool 이 아직 없습니다"})
	for _, refusal := range ScreenDraft(draft) {
		if refusal.Kind == RefusalNoJourney {
			t.Fatalf("a journey gate is present: %+v", refusal)
		}
	}
}
