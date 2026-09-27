package policy

import "testing"

func TestClassifyGateFailure(t *testing.T) {
	for _, tc := range []struct {
		name, status, output string
		kind                 GateFailureKind
		mode                 RepairMode
	}{
		{"go test failure", "FAILED", "--- FAIL: TestThing (0.01s)\n    thing_test.go:12: got 2 want 3", GateTestFailure, RepairCodeFix},
		{"compile error", "FAILED", "./main.go:12:2: undefined: doThing", GateBuildFailure, RepairCodeFix},
		{"coverage short", "FAILED", "total: (statements) 71.4%\n[gate coverage: measured 71.4 is below the threshold 85]", GateThresholdNotMet, RepairCodeFix},
		{"missing tool", "FAILED", "bash: pytest: command not found", GateEnvironment, RepairEnvironment},
		{"expired credentials", "FAILED", "remote: Invalid username or password.\nfatal: Authentication failed for 'https://example.com'", GateAuth, RepairEnvironment},
		{"dependency fetch", "FAILED", "npm ERR! 404 Not Found - GET https://registry.example/foo", GateDependency, RepairEnvironment},
		{"timeout", "TIMEOUT", "", GateTimeout, RepairHuman},
		{"unparseable measurement", "FAILED", "[gate coverage: value pattern matched no measurement in the output]", GateMisconfigured, RepairHuman},
		{"unrecognized", "FAILED", "something went sideways", GateUnknown, RepairHuman},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyGateFailure(tc.status, tc.output)
			if got.Kind != tc.kind || got.Mode != tc.mode {
				t.Fatalf("kind=%s mode=%s want kind=%s mode=%s", got.Kind, got.Mode, tc.kind, tc.mode)
			}
			if got.Summary == "" {
				t.Fatal("a classification must explain itself")
			}
		})
	}
}

// A broken environment also makes tests print failures; classifying that as a
// test failure would send an AI session to fix code that was never wrong.
func TestEnvironmentFailureIsNotMistakenForATestFailure(t *testing.T) {
	output := "--- FAIL: TestDatabase (0.00s)\n    db_test.go:9: dial tcp 127.0.0.1:5432: connection refused"
	if got := ClassifyGateFailure("FAILED", output); got.Mode != RepairEnvironment {
		t.Fatalf("kind=%s mode=%s", got.Kind, got.Mode)
	}
}
