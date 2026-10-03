package policy

import "testing"

// A test runner that found nothing to run is not a misconfigured gate. It is a
// test that has not been written yet, and that is a code fix.
//
// This mattered because the gate shape a goal draft produces —
// `go test -run ^TestRedirect$ ./shortener` — exits zero when the package has
// no test file. A criterion settled by that gate goes green the moment an empty
// package exists, so "goal complete" was reported for a feature nobody had
// implemented. Asserting that the named test ran turns the vacuous pass into a
// failure, and this is what that failure has to be classified as: the loop can
// write the missing test, while a human cannot fix anything about it.
func TestATestRunnerThatFoundNoTestsIsACodeFix(t *testing.T) {
	outputs := map[string]string{
		"go, no test files":   "?   \texample.com/v/shortener\t[no test files]\n[gate redirect: value pattern matched no measurement]",
		"go, no tests to run": "testing: warning: no tests to run\nPASS\nok  \texample.com/v/shortener\t0.001s",
		"go, ok no tests":     "ok  \texample.com/v/shortener\t0.002s [no tests to run]",
		"pytest, none":        "collected 0 items\n\n=========== no tests ran in 0.01s ===========",
		"vitest, none":        "No test files found, exiting with code 1",
	}
	for name, output := range outputs {
		failure := ClassifyGateFailure("FAILED", output)
		if failure.Mode != RepairCodeFix {
			t.Fatalf("%s: a missing test is written by the loop, not fixed by a person: %+v", name, failure)
		}
		if failure.Kind != GateNoTestsRan {
			t.Fatalf("%s: kind=%s", name, failure.Kind)
		}
		if failure.Summary == "" {
			t.Fatalf("%s: the session has to be told what to do", name)
		}
	}
}

// A genuinely misconfigured pattern is still a human's problem. Conflating the
// two would send the loop to write code about a broken regular expression.
func TestAnInvalidPatternIsStillAHumanProblem(t *testing.T) {
	failure := ClassifyGateFailure("FAILED", "[gate coverage: value pattern is invalid: missing closing ]]")
	if failure.Mode != RepairHuman {
		t.Fatalf("a broken pattern is not something the loop can write: %+v", failure)
	}
}

// And a pattern that matched nothing for some other reason — a coverage number
// the output does not carry — is still misconfigured. Only the no-tests case
// moves, because only that one names something the loop can create.
func TestAPatternThatMatchedNothingWithNoTestSignalIsMisconfigured(t *testing.T) {
	failure := ClassifyGateFailure("FAILED",
		"ok  \texample.com/v\t0.3s\n[gate coverage: value pattern matched no measurement]")
	if failure.Mode != RepairHuman {
		t.Fatalf("nothing here says a test is missing: %+v", failure)
	}
}

// A real test failure stays a test failure. The no-tests check must not swallow
// a run where tests existed and failed.
func TestARealTestFailureIsStillATestFailure(t *testing.T) {
	failure := ClassifyGateFailure("FAILED",
		"--- FAIL: TestRedirect (0.00s)\n    handler_test.go:12: want 302, got 404\nFAIL")
	if failure.Kind != GateTestFailure {
		t.Fatalf("tests ran and failed: %+v", failure)
	}
}

// And the case where both signals are in one output, which `go test ./...`
// produces routinely: one package has no test file and another package's test
// failed. The failure is what matters — a session told "write the missing test"
// would go and write one while an existing test is red.
func TestAFailureAlongsideAPackageWithNoTestsIsStillAFailure(t *testing.T) {
	output := "?   \texample.com/v/internal/config\t[no test files]\n" +
		"--- FAIL: TestRedirect (0.00s)\n    handler_test.go:12: want 302, got 404\n" +
		"FAIL\texample.com/v/shortener\t0.004s\nFAIL"
	failure := ClassifyGateFailure("FAILED", output)
	if failure.Kind == GateNoTestsRan {
		t.Fatalf("a test ran and failed; the empty package is incidental: %+v", failure)
	}
	if failure.Kind != GateTestFailure {
		t.Fatalf("kind=%s", failure.Kind)
	}
}
