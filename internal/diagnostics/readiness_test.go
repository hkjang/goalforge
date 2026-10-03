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

// The misconfiguration that looks healthiest: a criterion about whether the
// user's task works, measured by a gate that only compiles. Every other check
// is green, so readiness has to be the thing that says it.
func TestReadinessRejectsProofOfTheWrongKind(t *testing.T) {
	input := ReadinessInput{
		HasProject:       true,
		GoalTitle:        "notes",
		Criteria:         []string{"note_saves"},
		CriterionKinds:   map[string]string{"note_saves": "journey"},
		Gates:            []GateSpec{{Type: "note_saves", Command: []string{"go"}, Required: true, Kind: "build"}},
		BudgetConfigured: true,
	}
	if level := levelFor(CheckReadiness(input), "proof kind"); level != LevelFail {
		t.Fatalf("a build gate behind a journey criterion must block: %+v", CheckReadiness(input))
	}
	input.Gates[0].Kind = "journey"
	if level := levelFor(CheckReadiness(input), "proof kind"); level != LevelOK {
		t.Fatalf("a journey gate behind a journey criterion is fine: %+v", CheckReadiness(input))
	}
}

// The same misconfiguration one rung lower, and the one an AI session reaches
// for on its own: a gate that reports its judgement of the change standing
// behind a criterion that asked for the change to build. Readiness has to say
// so before the run, because afterwards it looks like proof.
func TestReadinessRejectsAJudgementBehindAnObjectiveCriterion(t *testing.T) {
	input := ReadinessInput{
		HasProject:       true,
		GoalTitle:        "service",
		Criteria:         []string{"build_passed"},
		CriterionKinds:   map[string]string{"build_passed": "build"},
		Gates:            []GateSpec{{Type: "build_passed", Command: []string{"go"}, Required: true, Kind: "review"}},
		BudgetConfigured: true,
	}
	if level := levelFor(CheckReadiness(input), "proof kind"); level != LevelFail {
		t.Fatalf("a review gate behind a build criterion must block: %+v", CheckReadiness(input))
	}
	input.Gates[0].Kind = "build"
	if level := levelFor(CheckReadiness(input), "proof kind"); level != LevelOK {
		t.Fatalf("a build gate behind a build criterion is fine: %+v", CheckReadiness(input))
	}
}

// A project whose gates only compile can complete without anything ever having
// been exercised. That is a warning rather than a failure: it can still finish,
// just on weaker evidence than the user probably thinks.
func TestReadinessWarnsWhenNothingChecksBehaviour(t *testing.T) {
	input := ReadinessInput{
		HasProject: true, GoalTitle: "svc", Criteria: []string{"build_passed"}, BudgetConfigured: true,
		Gates: []GateSpec{{Type: "build_passed", Command: []string{"go"}, Required: true, Kind: "build"}},
	}
	if level := levelFor(CheckReadiness(input), "proof kind"); level != LevelWarn {
		t.Fatalf("build-only verification must be called out: %+v", CheckReadiness(input))
	}
	input.Gates = append(input.Gates, GateSpec{Type: "tests_passed", Command: []string{"go"}, Required: true, Kind: "test"})
	if level := levelFor(CheckReadiness(input), "proof kind"); level != "" {
		t.Fatalf("a project that exercises its code needs no warning: %+v", CheckReadiness(input))
	}
}
