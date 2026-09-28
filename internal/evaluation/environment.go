// Package evaluation runs fixed tasks in clean, pinned environments so that a
// change to a prompt, a model, or the orchestrator can be told apart from the
// work that happened to come up. Attaching an existing run to a case measures
// that run; re-executing the case measures the configuration.
package evaluation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/goalforge/goalforge/internal/gitops"
)

// CaseSpec is everything needed to re-execute a task from a known state.
type CaseSpec struct {
	CaseID, Name, Kind string
	// Fixture is the repository the task starts from and Ref pins it to one
	// commit. A task that starts from "whatever is in the working copy" is not
	// a task anyone can re-run.
	Fixture, Ref string
	// CleanTreeID is the tree the fixture is expected to produce. A trial that
	// starts from anything else is measuring a different task.
	CleanTreeID              string
	GoalTitle, GoalObjective string
	Criteria                 []Criterion
	Gates                    []Gate
	// SeedWork is the backlog a trial starts with. A case that seeds its work
	// measures implementation; a case that leaves it empty measures the
	// configuration's own decomposition as well. Which one a suite is doing
	// has to be visible, so it is part of the pinned case rather than a
	// runtime default.
	SeedWork       []SeedWorkItem
	TokenBudget    int64
	CostBudgetUSD  float64
	TimeoutSeconds int
}

// Criterion is one completion condition. RequiredKind names the kind of check
// that can settle it, so a case can demand that a feature be exercised rather
// than merely compiled — and so both arms of a comparison are judged by it.
type Criterion struct{ Type, ExpectedValue, RequiredKind string }

// SeedWorkItem is one pre-decomposed task a trial begins with.
type SeedWorkItem struct {
	Title, Objective, Acceptance, ChangeScope string
	Priority                                  float64
	EstimatedTokens                           int64
}

type Gate struct {
	Type         string
	Command      []string
	Required     bool
	SuccessValue string
	ValuePattern string
	Timeout      int
	// Kind is what this gate establishes (build, test, journey, ...). It is
	// part of the pinned case because it decides which criteria the gate can
	// settle, and therefore what "passed" means for every trial.
	Kind string
}

// Environment is one prepared, disposable workspace for a single trial.
type Environment struct {
	Workspace string
	StateDB   string
	TreeID    string
	// Contaminated and ContaminationReason record a workspace that did not
	// start from the pinned state. The trial still gets recorded, marked
	// invalid: a silently discarded trial is how a suite starts reporting only
	// its favourable runs.
	Contaminated        bool
	ContaminationReason string
	cleanup             func()
}

// Close removes the workspace.
func (e Environment) Close() {
	if e.cleanup != nil {
		e.cleanup()
	}
}

// Prepare builds a clean workspace for one trial: a fresh clone of the fixture
// at its pinned ref and a state database that has never seen another trial.
// Reusing either is how an earlier attempt's answer, or its recorded state,
// leaks into the next measurement.
func Prepare(ctx context.Context, spec CaseSpec, root string) (Environment, error) {
	var env Environment
	if strings.TrimSpace(spec.Fixture) == "" {
		return env, errors.New("evaluation case needs a fixture repository to start from")
	}
	if _, err := os.Stat(filepath.Join(spec.Fixture, ".git")); err != nil {
		return env, fmt.Errorf("fixture %s is not a Git repository", spec.Fixture)
	}
	workspace, err := os.MkdirTemp(root, "trial-")
	if err != nil {
		return env, err
	}
	env.cleanup = func() { _ = os.RemoveAll(workspace) }
	// --no-hardlinks so the trial cannot reach back into the fixture's object
	// store, and a fresh directory so nothing survives from a previous trial.
	if output, cloneErr := exec.CommandContext(ctx, "git", "clone", "--no-hardlinks", "--quiet", spec.Fixture, workspace).CombinedOutput(); cloneErr != nil {
		env.Close()
		return env, fmt.Errorf("clone fixture: %w: %s", cloneErr, strings.TrimSpace(string(output)))
	}
	if ref := strings.TrimSpace(spec.Ref); ref != "" {
		if output, checkoutErr := exec.CommandContext(ctx, "git", "-C", workspace, "checkout", "--quiet", ref).CombinedOutput(); checkoutErr != nil {
			env.Close()
			return env, fmt.Errorf("checkout %s: %w: %s", ref, checkoutErr, strings.TrimSpace(string(output)))
		}
	}
	env.Workspace = workspace
	stateDir, err := os.MkdirTemp(root, "state-")
	if err != nil {
		env.Close()
		return env, err
	}
	env.StateDB = filepath.Join(stateDir, "state.db")
	previousCleanup := env.cleanup
	env.cleanup = func() {
		previousCleanup()
		_ = os.RemoveAll(stateDir)
	}
	env.TreeID, err = gitops.TreeID(ctx, workspace)
	if err != nil {
		env.Close()
		return env, fmt.Errorf("read prepared tree: %w", err)
	}
	// A prepared workspace with uncommitted content did not come from the
	// fixture alone.
	if strings.Contains(env.TreeID, "+") {
		env.Contaminated = true
		env.ContaminationReason = "준비된 작업 공간에 커밋되지 않은 파일이 있습니다"
		return env, nil
	}
	if expected := strings.TrimSpace(spec.CleanTreeID); expected != "" && expected != env.TreeID {
		env.Contaminated = true
		env.ContaminationReason = fmt.Sprintf("기준 상태 %s 를 기대했지만 %s 로 준비되었습니다", shortID(expected), shortID(env.TreeID))
	}
	return env, nil
}

// VerifyClean re-checks the workspace immediately before scoring. A trial that
// scores against files the task was supposed to produce, but which were
// already there, reports a success nobody earned.
func VerifyClean(ctx context.Context, spec CaseSpec, workspace string) error {
	if strings.TrimSpace(spec.CleanTreeID) == "" {
		return nil
	}
	tree, err := gitops.TreeID(ctx, workspace)
	if err != nil {
		return err
	}
	if tree != spec.CleanTreeID {
		return fmt.Errorf("workspace is not at the pinned clean state: expected %s, found %s", shortID(spec.CleanTreeID), shortID(tree))
	}
	return nil
}

func shortID(value string) string {
	if index := strings.LastIndex(value, "\x00"); index >= 0 {
		value = value[index+1:]
	}
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
