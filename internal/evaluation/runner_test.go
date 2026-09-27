package evaluation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.invalid"}, {"config", "user.name", "T"}} {
		if output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "fixture"}} {
		if output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(head))
}

type stubExecutor struct {
	outcome Outcome
	err     error
	seen    []string
	block   time.Duration
}

func (s *stubExecutor) Execute(ctx context.Context, env Environment, _ CaseSpec) (Outcome, error) {
	s.seen = append(s.seen, env.Workspace)
	if s.block > 0 {
		select {
		case <-ctx.Done():
			return s.outcome, ctx.Err()
		case <-time.After(s.block):
		}
	}
	return s.outcome, s.err
}

// Every repetition must start from the pinned state in its own workspace with
// its own state database. Reuse is how a previous attempt's answer, or its
// recorded state, leaks into the next measurement.
func TestRunnerGivesEachTrialACleanEnvironment(t *testing.T) {
	fixture, head := fixtureRepo(t)
	spec := CaseSpec{CaseID: "EVAL-1", Name: "fix", Kind: "bug_fix", Fixture: fixture, Ref: head}
	stub := &stubExecutor{outcome: Outcome{Completed: true, Tokens: 100, CostUSD: 0.01}}
	runner := Runner{Root: t.TempDir(), Executor: stub}
	trials, err := runner.Run(context.Background(), spec, "baseline", 3)
	if err != nil || len(trials) != 3 {
		t.Fatalf("trials=%d err=%v", len(trials), err)
	}
	for _, trial := range trials {
		if trial.Status != StatusPassed {
			t.Fatalf("trial=%+v", trial)
		}
		if trial.ConditionHash == "" || trial.CleanTreeID == "" {
			t.Fatalf("a trial must record what it ran under: %+v", trial)
		}
	}
	if len(stub.seen) != 3 || stub.seen[0] == stub.seen[1] || stub.seen[1] == stub.seen[2] {
		t.Fatalf("repetitions shared a workspace: %v", stub.seen)
	}
	// The workspaces are disposed of rather than left to accumulate.
	for _, workspace := range stub.seen {
		if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
			t.Fatalf("workspace %s survived the trial", workspace)
		}
	}
}

// AT-15: a workspace that did not start from the pinned state is measuring a
// different task. The trial is recorded as invalid rather than scored, and
// rather than silently dropped.
func TestRunnerInvalidatesContaminatedEnvironments(t *testing.T) {
	fixture, head := fixtureRepo(t)
	// A case pinned to a clean state that the fixture no longer produces:
	// the previous run's answer was committed into the fixture.
	if err := os.WriteFile(filepath.Join(fixture, "answer.go"), []byte("package main\n// solved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "leaked answer"}} {
		if output, err := exec.Command("git", append([]string{"-C", fixture}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	spec := CaseSpec{CaseID: "EVAL-1", Fixture: fixture, CleanTreeID: head}
	stub := &stubExecutor{outcome: Outcome{Completed: true}}
	runner := Runner{Root: t.TempDir(), Executor: stub}
	trials, err := runner.Run(context.Background(), spec, "baseline", 1)
	if err != nil {
		t.Fatal(err)
	}
	if trials[0].Status != StatusInvalid {
		t.Fatalf("a contaminated environment must invalidate the trial: %+v", trials[0])
	}
	if !strings.Contains(trials[0].Detail, "기준 상태") {
		t.Fatalf("the trial must say what was wrong: %q", trials[0].Detail)
	}
	if len(stub.seen) != 0 {
		t.Fatal("a contaminated environment must not be executed in")
	}
}

// Budget and time are part of the condition: a result produced by spending
// more than the comparison allowed is not comparable.
func TestRunnerEnforcesBudgetAndTime(t *testing.T) {
	fixture, head := fixtureRepo(t)
	base := CaseSpec{CaseID: "EVAL-1", Fixture: fixture, Ref: head}

	overBudget := base
	overBudget.TokenBudget = 50
	runner := Runner{Root: t.TempDir(), Executor: &stubExecutor{outcome: Outcome{Completed: true, Tokens: 500}}}
	trials, err := runner.Run(context.Background(), overBudget, "baseline", 1)
	if err != nil || trials[0].Status != StatusBudget {
		t.Fatalf("exceeding the token budget is not a pass: %+v err=%v", trials[0], err)
	}

	overCost := base
	overCost.CostBudgetUSD = 0.01
	runner = Runner{Root: t.TempDir(), Executor: &stubExecutor{outcome: Outcome{Completed: true, CostUSD: 5}}}
	trials, err = runner.Run(context.Background(), overCost, "baseline", 1)
	if err != nil || trials[0].Status != StatusBudget {
		t.Fatalf("exceeding the cost budget is not a pass: %+v err=%v", trials[0], err)
	}

	timed := base
	timed.TimeoutSeconds = 1
	runner = Runner{Root: t.TempDir(), Executor: &stubExecutor{block: 3 * time.Second}}
	trials, err = runner.Run(context.Background(), timed, "baseline", 1)
	if err != nil || trials[0].Status != StatusTimeout {
		t.Fatalf("a trial past its time limit is a timeout: %+v err=%v", trials[0], err)
	}
}

// An executor that fails is an error, not a failed task: the configuration was
// never measured, and scoring it as a failure would misreport the suite.
func TestRunnerSeparatesErrorsFromFailures(t *testing.T) {
	fixture, head := fixtureRepo(t)
	spec := CaseSpec{CaseID: "EVAL-1", Fixture: fixture, Ref: head}
	runner := Runner{Root: t.TempDir(), Executor: &stubExecutor{err: errors.New("provider unavailable")}}
	trials, _ := runner.Run(context.Background(), spec, "baseline", 1)
	if trials[0].Status != StatusError || !strings.Contains(trials[0].Detail, "provider unavailable") {
		t.Fatalf("trial=%+v", trials[0])
	}
	runner = Runner{Root: t.TempDir(), Executor: &stubExecutor{outcome: Outcome{Completed: false}}}
	trials, _ = runner.Run(context.Background(), spec, "baseline", 1)
	if trials[0].Status != StatusFailed {
		t.Fatalf("an incomplete task is a failure: %+v", trials[0])
	}
}

// The condition hash has to change when anything that affects the result
// changes, or trials run under different conditions get averaged together.
func TestConditionHashCoversWhatAffectsTheResult(t *testing.T) {
	base := CaseSpec{Fixture: "/repo", Ref: "abc", Criteria: []Criterion{{Type: "build", ExpectedValue: "true"}},
		Gates: []Gate{{Type: "build", Command: []string{"go", "build"}, Required: true}}, TokenBudget: 1000, TimeoutSeconds: 60}
	original := ConditionHash(base, "baseline")
	if original == "" {
		t.Fatal("no hash produced")
	}
	if ConditionHash(base, "variant") == original {
		t.Error("the label must change the condition")
	}
	for name, mutate := range map[string]func(*CaseSpec){
		"ref":      func(s *CaseSpec) { s.Ref = "def" },
		"budget":   func(s *CaseSpec) { s.TokenBudget = 2000 },
		"timeout":  func(s *CaseSpec) { s.TimeoutSeconds = 120 },
		"gate":     func(s *CaseSpec) { s.Gates[0].Command = []string{"true"} },
		"criteria": func(s *CaseSpec) { s.Criteria[0].ExpectedValue = "false" },
	} {
		changed := base
		changed.Gates = append([]Gate(nil), base.Gates...)
		changed.Criteria = append([]Criterion(nil), base.Criteria...)
		mutate(&changed)
		if ConditionHash(changed, "baseline") == original {
			t.Errorf("changing the %s must change the condition hash", name)
		}
	}
	// Ordering is not a condition.
	reordered := base
	reordered.Gates = append([]Gate(nil), base.Gates...)
	reordered.Gates = append(reordered.Gates, Gate{Type: "aaa", Command: []string{"x"}})
	shuffled := reordered
	shuffled.Gates = []Gate{{Type: "aaa", Command: []string{"x"}}, base.Gates[0]}
	if ConditionHash(reordered, "baseline") != ConditionHash(shuffled, "baseline") {
		t.Error("gate ordering must not change the condition")
	}
}
