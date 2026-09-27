package evaluation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Trial statuses. INVALID is deliberately distinct from FAILED: a trial that
// could not be measured says nothing about the configuration, and counting it
// as a failure is as wrong as discarding it.
const (
	StatusPassed  = "PASSED"
	StatusFailed  = "FAILED"
	StatusTimeout = "TIMEOUT"
	StatusBudget  = "BUDGET_EXCEEDED"
	StatusInvalid = "INVALID"
	StatusError   = "ERROR"
)

// Outcome is what an executor reports about one trial's work.
type Outcome struct {
	Completed     bool
	RunIDs        []string
	Tokens        int64
	CostUSD       float64
	Interventions int
	// Detail explains the stopping condition in the user's terms.
	Detail string
}

// Executor performs a trial's work inside a prepared environment. It is an
// interface so a suite can be exercised without a provider: the runner's own
// behaviour — clean state, limits, invalidation — is what these tests are
// about, and binding it to a live model would make them untestable.
type Executor interface {
	Execute(ctx context.Context, env Environment, spec CaseSpec) (Outcome, error)
}

// Trial is one measured execution of one case under one label.
type Trial struct {
	CaseID, Label   string
	Repetition      int
	ConditionHash   string
	CleanTreeID     string
	Status, Detail  string
	Completed       bool
	Tokens          int64
	CostUSD         float64
	Interventions   int
	DurationSeconds float64
	StartedAt       time.Time
}

// Runner executes cases repeatedly in disposable environments.
type Runner struct {
	Root     string
	Executor Executor
}

// RunTrial prepares an environment, executes the case in it, and reports the
// measurement. Contamination is recorded as an invalid trial rather than
// discarded, and rather than scored: both would misreport the suite.
func (r Runner) RunTrial(ctx context.Context, spec CaseSpec, label string, repetition int) (Trial, error) {
	trial := Trial{CaseID: spec.CaseID, Label: label, Repetition: repetition,
		ConditionHash: ConditionHash(spec, label), StartedAt: time.Now().UTC()}
	if r.Executor == nil {
		return trial, errors.New("evaluation runner needs an executor")
	}
	root := r.Root
	if root == "" {
		root = os.TempDir()
	}
	env, err := Prepare(ctx, spec, root)
	if err != nil {
		trial.Status, trial.Detail = StatusError, err.Error()
		return trial, nil
	}
	defer env.Close()
	trial.CleanTreeID = env.TreeID
	if env.Contaminated {
		trial.Status, trial.Detail = StatusInvalid, env.ContaminationReason
		return trial, nil
	}
	runCtx := ctx
	if spec.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	outcome, execErr := r.Executor.Execute(runCtx, env, spec)
	trial.DurationSeconds = time.Since(trial.StartedAt).Seconds()
	trial.Tokens, trial.CostUSD, trial.Interventions = outcome.Tokens, outcome.CostUSD, outcome.Interventions
	trial.Completed = outcome.Completed
	trial.Detail = outcome.Detail
	switch {
	case execErr != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded):
		trial.Status = StatusTimeout
		trial.Detail = fmt.Sprintf("%d초 제한을 넘겼습니다", spec.TimeoutSeconds)
	case execErr != nil:
		trial.Status, trial.Detail = StatusError, execErr.Error()
	case spec.TokenBudget > 0 && outcome.Tokens > spec.TokenBudget:
		trial.Status = StatusBudget
		trial.Detail = fmt.Sprintf("토큰 %d 가 예산 %d 를 넘었습니다", outcome.Tokens, spec.TokenBudget)
	case spec.CostBudgetUSD > 0 && outcome.CostUSD > spec.CostBudgetUSD:
		trial.Status = StatusBudget
		trial.Detail = fmt.Sprintf("비용 $%.4f 가 예산 $%.4f 를 넘었습니다", outcome.CostUSD, spec.CostBudgetUSD)
	case outcome.Completed:
		trial.Status = StatusPassed
	default:
		trial.Status = StatusFailed
		if trial.Detail == "" {
			trial.Detail = "필수 완료 조건을 충족하지 못했습니다"
		}
	}
	return trial, nil
}

// Run executes a case the requested number of times. Every repetition is
// returned, including the ones that failed or could not be measured: reporting
// only the favourable runs is the failure mode these numbers exist to prevent.
func (r Runner) Run(ctx context.Context, spec CaseSpec, label string, repetitions int) ([]Trial, error) {
	if repetitions <= 0 {
		repetitions = 1
	}
	trials := make([]Trial, 0, repetitions)
	for i := 1; i <= repetitions; i++ {
		trial, err := r.RunTrial(ctx, spec, label, i)
		if err != nil {
			return trials, err
		}
		trials = append(trials, trial)
		if ctx.Err() != nil {
			break
		}
	}
	return trials, nil
}
