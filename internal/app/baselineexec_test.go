package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/evaluation"
	"github.com/goalforge/goalforge/internal/provider"
)

// baselineProvider stands in for a coding CLI: it runs a canned effect on the
// workspace and reports usage, so the baseline arm's own behaviour — same
// gates, same judge — is what the test is about.
type baselineProvider struct {
	name   string
	effect func(workDir string) error
	usage  provider.Usage
	prompt string
}

func (p *baselineProvider) Name() string                        { return p.name }
func (p *baselineProvider) Capabilities() provider.Capabilities { return provider.Capabilities{} }
func (p *baselineProvider) Start(_ context.Context, request provider.RunRequest) (<-chan provider.Event, error) {
	p.prompt = request.Prompt
	events := make(chan provider.Event, 3)
	go func() {
		defer close(events)
		if p.effect != nil {
			if err := p.effect(request.WorkDir); err != nil {
				events <- provider.Event{Type: provider.EventFailed, Message: err.Error()}
				return
			}
		}
		usage := p.usage
		events <- provider.Event{Type: provider.EventUsage, Usage: &usage}
		events <- provider.Event{Type: provider.EventCompleted}
	}()
	return events, nil
}
func (p *baselineProvider) Resume(context.Context, string, provider.RunRequest) (<-chan provider.Event, error) {
	return nil, nil
}
func (p *baselineProvider) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	return provider.QuotaSnapshot{}, nil
}
func (p *baselineProvider) Interrupt(context.Context, string) error { return nil }

func baselineEnv(t *testing.T) evaluation.Environment {
	t.Helper()
	workspace := t.TempDir()
	return evaluation.Environment{Workspace: workspace, StateDB: filepath.Join(t.TempDir(), "state.db")}
}

func gateScript(t *testing.T, dir, name, body string) []string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{path}
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs a POSIX shell to write gate scripts")
	}
}

// The baseline is judged by exactly the gates GoalForge is judged by. A
// session that actually satisfies them passes.
func TestBaselinePassesWhenItSatisfiesTheSameGates(t *testing.T) {
	skipOnWindows(t)
	env := baselineEnv(t)
	scripts := t.TempDir()
	spec := evaluation.CaseSpec{CaseID: "EVAL-1", GoalTitle: "add the marker file",
		GoalObjective: "create done.txt in the workspace",
		Criteria:      []evaluation.Criterion{{Type: "marker", ExpectedValue: "true"}},
		Gates: []evaluation.Gate{{Type: "marker", Required: true, SuccessValue: "true", Timeout: 30,
			Command: gateScript(t, scripts, "marker", `test -f "$PWD/done.txt"`)}}}
	fake := &baselineProvider{name: "codex", usage: provider.Usage{OutputTokens: 120, CostUSD: 0.5},
		effect: func(workDir string) error { return os.WriteFile(filepath.Join(workDir, "done.txt"), []byte("ok"), 0o600) }}
	outcome, err := BaselineExecutor{Provider: fake, Model: "haiku"}.Execute(context.Background(), env, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Completed {
		t.Fatalf("the gate is satisfied: %+v", outcome)
	}
	if outcome.Tokens != 120 || outcome.CostUSD != 0.5 {
		t.Fatalf("the baseline's cost must be measured too: %+v", outcome)
	}
}

// A session that does nothing fails the same gate. Without this the baseline
// would flatter itself and the comparison would understate GoalForge.
func TestBaselineFailsWhenTheGateIsNotSatisfied(t *testing.T) {
	skipOnWindows(t)
	env := baselineEnv(t)
	scripts := t.TempDir()
	spec := evaluation.CaseSpec{CaseID: "EVAL-1", GoalTitle: "add the marker file",
		Criteria: []evaluation.Criterion{{Type: "marker", ExpectedValue: "true"}},
		Gates: []evaluation.Gate{{Type: "marker", Required: true, SuccessValue: "true", Timeout: 30,
			Command: gateScript(t, scripts, "marker", `test -f "$PWD/done.txt"`)}}}
	fake := &baselineProvider{name: "codex"}
	outcome, err := BaselineExecutor{Provider: fake, Model: "haiku"}.Execute(context.Background(), env, spec)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Completed {
		t.Fatal("a session that changed nothing must not be judged complete")
	}
	if !strings.Contains(outcome.Detail, "marker") {
		t.Fatalf("the user must be told which criterion failed: %q", outcome.Detail)
	}
}

// The kind rule applies to the baseline identically. If it did not, one arm
// could pass on evidence the other arm is refused.
func TestBaselineIsHeldToTheSameProofKind(t *testing.T) {
	skipOnWindows(t)
	env := baselineEnv(t)
	scripts := t.TempDir()
	spec := evaluation.CaseSpec{CaseID: "EVAL-1", GoalTitle: "save a note",
		Criteria: []evaluation.Criterion{{Type: "saves", ExpectedValue: "true", RequiredKind: "journey"}},
		Gates: []evaluation.Gate{{Type: "saves", Required: true, SuccessValue: "true", Timeout: 30, Kind: "build",
			Command: gateScript(t, scripts, "build", `exit 0`)}}}
	fake := &baselineProvider{name: "codex"}
	outcome, err := BaselineExecutor{Provider: fake, Model: "haiku"}.Execute(context.Background(), env, spec)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Completed {
		t.Fatal("a build gate must not settle a journey criterion for the baseline either")
	}
	if !strings.Contains(outcome.Detail, "종류") {
		t.Fatalf("the mismatch must be named: %q", outcome.Detail)
	}
}

// The baseline gets the same task and the same checks GoalForge gets.
// Withholding them would measure GoalForge's prompt rather than its
// orchestration, and make the comparison flattering rather than true.
func TestBaselinePromptCarriesTheTaskAndTheChecks(t *testing.T) {
	spec := evaluation.CaseSpec{GoalTitle: "메모 저장", GoalObjective: "사용자가 메모를 저장할 수 있게 한다",
		Criteria: []evaluation.Criterion{{Type: "saves", ExpectedValue: "true", RequiredKind: "journey"}},
		Gates:    []evaluation.Gate{{Type: "saves", Command: []string{"./scripts/journey.sh"}, Required: true}}}
	prompt := BaselinePrompt(spec)
	for _, want := range []string{"메모 저장", "사용자가 메모를 저장할 수 있게 한다", "./scripts/journey.sh", "saves", "journey"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the baseline prompt must carry %q:\n%s", want, prompt)
		}
	}
}

// A case with no gates cannot judge either arm, so the baseline refuses rather
// than reporting a result it cannot support.
func TestBaselineRefusesACaseItCannotJudge(t *testing.T) {
	env := baselineEnv(t)
	fake := &baselineProvider{name: "codex"}
	_, err := BaselineExecutor{Provider: fake}.Execute(context.Background(), env,
		evaluation.CaseSpec{Criteria: []evaluation.Criterion{{Type: "x", ExpectedValue: "true"}}})
	if err == nil {
		t.Fatal("a case with no gates must be refused")
	}
}
