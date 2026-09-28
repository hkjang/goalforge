package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goalforge/goalforge/internal/evaluation"
	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// ServiceExecutor drives real GoalForge runs inside a prepared trial: it
// registers the case's goal, criteria, and gates in the trial's own database,
// then works the backlog until the goal completes or a limit stops it.
type ServiceExecutor struct {
	Provider, Model string
	NewSession      evaluation.SessionFactory
	// MaxWorkItems bounds a single trial's loop. A task that never finishes
	// must end as a failure rather than run until the suite is abandoned.
	MaxWorkItems int
}

func (e ServiceExecutor) Execute(ctx context.Context, env evaluation.Environment, spec evaluation.CaseSpec) (evaluation.Outcome, error) {
	var outcome evaluation.Outcome
	if e.NewSession == nil {
		return outcome, errors.New("evaluation executor needs a session factory")
	}
	db, err := store.Open(env.StateDB)
	if err != nil {
		return outcome, err
	}
	defer db.Close()
	project := model.Project{Name: "eval-" + spec.Name, RepositoryPath: env.Workspace, DefaultBranch: "main",
		Provider: e.Provider, Model: e.Model, WorktreeEnabled: true, AutoCommitEnabled: true, WIPLimit: 1}
	if err = db.CreateProject(ctx, project); err != nil {
		return outcome, err
	}
	registered, err := db.ProjectByPath(ctx, env.Workspace)
	if err != nil {
		return outcome, err
	}
	criteria := make([]model.Criterion, 0, len(spec.Criteria))
	for _, criterion := range spec.Criteria {
		criteria = append(criteria, model.Criterion{Type: criterion.Type, ExpectedValue: criterion.ExpectedValue,
			RequiredKind: criterion.RequiredKind})
	}
	if len(criteria) == 0 {
		return outcome, errors.New("evaluation case needs completion criteria; without them nothing can judge the trial")
	}
	goal, err := db.SetGoal(ctx, registered.ID, spec.GoalTitle, spec.GoalObjective, "", criteria)
	if err != nil {
		return outcome, err
	}
	for _, seed := range spec.SeedWork {
		if _, err = db.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT", Title: seed.Title,
			Objective: seed.Objective, Acceptance: seed.Acceptance, ChangeScope: seed.ChangeScope,
			Priority: seed.Priority, EstimatedTokens: seed.EstimatedTokens}); err != nil {
			return outcome, err
		}
	}
	for _, gate := range spec.Gates {
		timeout := gateTimeout(gate)
		if err = db.UpsertGate(ctx, registered.ID, store.GateConfig{Type: gate.Type, Command: gate.Command,
			Timeout: timeout, Required: gate.Required, SuccessValue: gate.SuccessValue,
			ValuePattern: gate.ValuePattern, Kind: gate.Kind}); err != nil {
			return outcome, err
		}
	}
	if spec.TokenBudget > 0 || spec.CostBudgetUSD > 0 {
		if err = db.SetProjectBudget(ctx, registered.ID, spec.TokenBudget, spec.CostBudgetUSD); err != nil {
			return outcome, err
		}
	}
	session, err := e.NewSession(ctx, env, registered)
	if err != nil {
		return outcome, err
	}
	defer session.Close()
	limit := e.MaxWorkItems
	if limit <= 0 {
		limit = 20
	}
	for attempt := 0; attempt < limit; attempt++ {
		current, projectErr := db.ProjectByID(ctx, registered.ID)
		if projectErr != nil {
			return outcome, projectErr
		}
		progress, runErr := session.Continue(ctx, current)
		outcome.Tokens += progress.Tokens
		outcome.CostUSD += progress.CostUSD
		if progress.RunID != "" {
			outcome.RunIDs = append(outcome.RunIDs, progress.RunID)
		}
		switch {
		case errors.Is(runErr, store.ErrNotFound):
			// No executable work left; the criteria decide the rest.
			outcome.Detail = "실행할 수 있는 작업이 없습니다"
			return e.finish(ctx, db, registered.ID, outcome)
		case runErr != nil:
			outcome.Detail = runErr.Error()
			return e.finish(ctx, db, registered.ID, outcome)
		case progress.Completed:
			return e.finish(ctx, db, registered.ID, outcome)
		}
		if ctx.Err() != nil {
			return outcome, ctx.Err()
		}
	}
	outcome.Detail = fmt.Sprintf("작업 %d건을 넘겨 중단했습니다", limit)
	return e.finish(ctx, db, registered.ID, outcome)
}

// finish decides completion from the trial's own recorded evidence rather than
// from what the session reported, which is the same rule the product applies.
func (e ServiceExecutor) finish(ctx context.Context, db *store.Store, projectID string, outcome evaluation.Outcome) (evaluation.Outcome, error) {
	goal, err := db.CurrentGoal(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		goal, err = db.LatestGoal(ctx, projectID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return outcome, nil
	}
	if err != nil {
		return outcome, err
	}
	detail, err := db.GoalProgressDetail(ctx, goal)
	if err != nil {
		return outcome, err
	}
	outcome.Completed = detail.Complete
	if !detail.Complete && outcome.Detail == "" {
		for _, criterion := range detail.Criteria {
			if criterion.Status != "MET" {
				outcome.Detail = fmt.Sprintf("완료 조건 %s 미충족 (%s)", criterion.Type, criterion.Status)
				break
			}
		}
	}
	return outcome, nil
}

// gateTimeout is shared by both arms so the baseline is never given a
// different amount of time to pass the same check.
func gateTimeout(gate evaluation.Gate) time.Duration {
	if gate.Timeout > 0 {
		return time.Duration(gate.Timeout) * time.Second
	}
	return 10 * time.Minute
}
