package observer

import (
	"strings"
	"testing"
)

// The hole the user hit. `go test -run ^TestRedirect$ ./shortener` exits zero
// when the package has no test file:
//
//	?   example.com/v/shortener  [no test files]
//	exit=0
//
// At draft time the package does not exist, so the gate fails and the "fails
// now" rule is satisfied. Then a session creates the package, writes no test,
// and the gate passes — the criterion goes green and the goal is reported
// complete for a feature nobody implemented. "Fails now" proved the gate fails
// when the directory is missing, which is much weaker than "fails until the
// feature works".
//
// A test-runner gate therefore has to assert that the named test actually ran.
func TestATestGateMustAssertTheTestRan(t *testing.T) {
	draft := draftOf(DraftCriterion{Type: "redirect_behavior", ExpectedValue: "true", Kind: "test",
		GateCommand:   []string{"go", "test", "-count=1", "-run", "^TestRedirect$", "./shortener"},
		WhyItFailsNow: "shortener 패키지가 아직 없습니다"})
	refusals := ScreenDraft(draft)
	var found bool
	for _, refusal := range refusals {
		if refusal.Kind == RefusalVacuousPass {
			found = true
			if !strings.Contains(refusal.Detail, "value_pattern") {
				t.Fatalf("the refusal must say what to add: %q", refusal.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("this gate passes once an empty package exists: %+v", refusals)
	}
}

// With the assertion it is accepted: -v so the runner names what it ran, and a
// pattern that only matches when the named test passed.
func TestATestGateThatAssertsTheTestRanIsAccepted(t *testing.T) {
	criterion := DraftCriterion{Type: "redirect_behavior", ExpectedValue: "TestRedirect", Kind: "test",
		GateCommand:   []string{"go", "test", "-count=1", "-v", "-run", "^TestRedirect$", "./shortener"},
		ValuePattern:  `--- PASS: (TestRedirect)`,
		WhyItFailsNow: "shortener 패키지가 아직 없습니다"}
	refusals := ScreenDraft(draftOf(criterion, DraftCriterion{Type: "server_starts", ExpectedValue: "true", Kind: "journey",
		GateCommand: []string{"go", "run", "./cmd/shortener", "--check"}, ValuePattern: "listening",
		WhyItFailsNow: "cmd/shortener 가 아직 없습니다"}))
	for _, refusal := range refusals {
		if refusal.Kind == RefusalVacuousPass {
			t.Fatalf("this gate cannot pass without the test: %+v", refusal)
		}
	}
	if len(refusals) != 0 {
		t.Fatalf("refusals=%+v", refusals)
	}
}

// A gate that is not a test runner is not held to this. `test -f notes.go` or a
// build command cannot pass vacuously in the same way, and demanding a pattern
// from them would make the drafter invent one.
func TestANonRunnerGateNeedsNoPattern(t *testing.T) {
	for _, command := range [][]string{
		{"go", "build", "./..."},
		{"test", "-f", "notes.go"},
		{"./scripts/journey.sh"},
	} {
		draft := draftOf(DraftCriterion{Type: "something", ExpectedValue: "true", Kind: "build",
			GateCommand: command, WhyItFailsNow: "아직 만들지 않았습니다"})
		for _, refusal := range ScreenDraft(draft) {
			if refusal.Kind == RefusalVacuousPass {
				t.Fatalf("%v is not a filtered test run: %+v", command, refusal)
			}
		}
	}
}

// A pattern that does not mention the test being filtered for is not an
// assertion that it ran. It would match some other line and the gate would pass
// vacuously again, with a pattern attached to make it look checked.
func TestThePatternMustNameTheFilteredTest(t *testing.T) {
	draft := draftOf(DraftCriterion{Type: "redirect_behavior", ExpectedValue: "ok", Kind: "test",
		GateCommand:   []string{"go", "test", "-v", "-run", "^TestRedirect$", "./shortener"},
		ValuePattern:  `(ok)\s`,
		WhyItFailsNow: "아직 없습니다"})
	var found bool
	for _, refusal := range ScreenDraft(draft) {
		if refusal.Kind == RefusalVacuousPass {
			found = true
		}
	}
	if !found {
		t.Fatalf(`a pattern matching "ok" passes on "[no test files]" output too: %+v`, ScreenDraft(draft))
	}
}

// And a filtered run with no -v cannot show which test ran, so a pattern on it
// is unenforceable whatever it says.
func TestAFilteredRunWithoutVerboseIsRefused(t *testing.T) {
	draft := draftOf(DraftCriterion{Type: "redirect_behavior", ExpectedValue: "TestRedirect", Kind: "test",
		GateCommand:   []string{"go", "test", "-run", "^TestRedirect$", "./shortener"},
		ValuePattern:  `--- PASS: (TestRedirect)`,
		WhyItFailsNow: "아직 없습니다"})
	var found bool
	for _, refusal := range ScreenDraft(draft) {
		if refusal.Kind == RefusalVacuousPass {
			found = true
		}
	}
	if !found {
		t.Fatal("without -v the runner never prints which test passed")
	}
}

// An unfiltered run passes on a package with no test file just as a filtered
// one does — `go test ./...` prints "[no test files]" and exits zero — so it
// needs the assertion too. Only the filter changes what the assertion looks
// like, not whether one is needed.
func TestAnUnfilteredTestRunAlsoNeedsAnAssertion(t *testing.T) {
	draft := draftOf(DraftCriterion{Type: "suite_passes", ExpectedValue: "true", Kind: "test",
		GateCommand: []string{"go", "test", "./..."}, WhyItFailsNow: "아직 테스트가 없습니다"})
	var found bool
	for _, refusal := range ScreenDraft(draft) {
		if refusal.Kind == RefusalVacuousPass {
			found = true
		}
	}
	if !found {
		t.Fatal(`go test ./... passes on a tree with no tests at all`)
	}
	// With a pattern proving something ran, it is accepted. An unfiltered run
	// has no single name to assert, so any pattern that only matches real
	// output will do.
	withPattern := draftOf(DraftCriterion{Type: "suite_passes", ExpectedValue: "3", Kind: "test",
		GateCommand:   []string{"go", "test", "-v", "./..."},
		ValuePattern:  `--- PASS: \w+`,
		WhyItFailsNow: "아직 테스트가 없습니다"})
	for _, refusal := range ScreenDraft(withPattern) {
		if refusal.Kind == RefusalVacuousPass {
			t.Fatalf("this one proves a test ran: %+v", refusal)
		}
	}
}

// The pattern has to survive into the installed gate. It is what makes the gate
// fail when the test does not exist, and dropping it on the way through would
// reinstate the vacuous pass with the screen still reporting the gate as
// checked.
func TestTheAssertionSurvivesIntoTheInstalledGate(t *testing.T) {
	draft := draftOf(DraftCriterion{Type: "redirect_behavior", ExpectedValue: "TestRedirect", Kind: "test",
		GateCommand:   []string{"go", "test", "-v", "-run", "^TestRedirect$", "./shortener"},
		ValuePattern:  `--- PASS: (TestRedirect)`,
		WhyItFailsNow: "아직 없습니다"})
	var checked bool
	for _, gate := range draft.Gates() {
		if gate.Type != "redirect_behavior" {
			continue
		}
		checked = true
		if gate.ValuePattern != `--- PASS: (TestRedirect)` {
			t.Fatalf("the assertion must reach the gate: %+v", gate)
		}
	}
	if !checked {
		t.Fatal("the criterion must become a gate")
	}
}
