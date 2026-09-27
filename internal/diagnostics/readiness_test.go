package diagnostics

import (
	"strings"
	"testing"
)

func levelFor(checks []Check, name string) string {
	for _, check := range checks {
		if check.Name == name {
			return check.Level
		}
	}
	return ""
}

// The configuration that looks healthy and never completes: a criterion with
// no gate of the same name can never accumulate evidence.
func TestReadinessCatchesUnmeasurableCriteria(t *testing.T) {
	input := ReadinessInput{
		HasProject: true,
		GoalTitle:  "ship",
		Criteria:   []string{"build_passed", "latency_p95"},
		Gates: []GateSpec{
			{Type: "build_passed", Command: []string{"go"}, Required: true},
		},
		BudgetConfigured: true,
	}
	checks := CheckReadiness(input)
	if levelFor(checks, "criteria coverage") != LevelFail {
		t.Fatalf("an unmeasurable criterion must block: %+v", checks)
	}
	for _, check := range checks {
		if check.Name == "criteria coverage" && !strings.Contains(check.Detail, "latency_p95") {
			t.Fatalf("the check must name the criterion: %q", check.Detail)
		}
	}
	input.Gates = append(input.Gates, GateSpec{Type: "latency_p95", Command: []string{"go"}, Required: true})
	if levelFor(CheckReadiness(input), "criteria coverage") != LevelOK {
		t.Fatal("covering the criterion must clear the finding")
	}
}

func TestReadinessBlocksConfigurationsThatCannotFinish(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input ReadinessInput
		check string
	}{
		{"no goal", ReadinessInput{HasProject: true}, "goal"},
		{"no criteria", ReadinessInput{HasProject: true, GoalTitle: "g"}, "criteria"},
		{"no gates", ReadinessInput{HasProject: true, GoalTitle: "g", Criteria: []string{"build_passed"}}, "gates"},
		{"no required gate", ReadinessInput{HasProject: true, GoalTitle: "g", Criteria: []string{"build_passed"},
			Gates: []GateSpec{{Type: "build_passed", Command: []string{"go"}}}}, "gates"},
		{"missing command", ReadinessInput{HasProject: true, GoalTitle: "g", Criteria: []string{"build_passed"},
			Gates: []GateSpec{{Type: "build_passed", Command: []string{"goalforge-not-a-real-binary"}, Required: true}}}, "gate commands"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if level := levelFor(CheckReadiness(tc.input), tc.check); level != LevelFail {
				t.Fatalf("%s should be FAIL, got %q in %+v", tc.check, level, CheckReadiness(tc.input))
			}
		})
	}
}

// Readiness says nothing about a directory with no project: that is the
// environment check's job, and duplicating it would report the same problem
// twice with different wording.
func TestReadinessIsSilentWithoutAProject(t *testing.T) {
	if checks := CheckReadiness(ReadinessInput{}); len(checks) != 0 {
		t.Fatalf("checks=%+v", checks)
	}
}

// Things that need attention but do not make completion impossible are
// warnings, so a green doctor still means "this can finish".
func TestReadinessWarnsWithoutBlocking(t *testing.T) {
	input := ReadinessInput{HasProject: true, GoalTitle: "g", Criteria: []string{"build_passed"},
		Gates:              []GateSpec{{Type: "build_passed", Command: []string{"go"}, Required: true}},
		IntegrationPending: true, IntegrationReason: "병합 후", StaleCriteria: []string{"build_passed"}}
	checks := CheckReadiness(input)
	for _, name := range []string{"budget", "integration", "evidence"} {
		if levelFor(checks, name) != LevelWarn {
			t.Errorf("%s should warn, got %q", name, levelFor(checks, name))
		}
	}
	for _, check := range checks {
		if check.Level == LevelFail {
			t.Errorf("nothing here makes completion impossible: %+v", check)
		}
	}
}
