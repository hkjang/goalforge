package sqlite

import (
	"context"
	"errors"
	"time"
)

// ReproductionPackage is everything needed to put a failure back in front of a
// person under the same conditions: the commit it was based on, the workspace
// it ran in, the commands that were executed, and what they printed. It does
// not try to make the model produce the same output again — that is not
// reproducible and not what a developer investigating a failure needs.
type ReproductionPackage struct {
	RunID, ProjectID, WorkItemID string
	Repository, Worktree, Branch string
	BaseCommit                   string
	Provider, Model, TaskType    string
	State                        string
	StartedAt, EndedAt           time.Time
	Prompt                       PromptView
	Gates                        []GateConfig
	Results                      []VerificationRecord
	FileChanges                  []string
	Repair                       RepairPlan
}

// BuildReproductionPackage assembles the package for a run.
func (s *Store) BuildReproductionPackage(ctx context.Context, projectID, runID string) (ReproductionPackage, error) {
	pkg := ReproductionPackage{RunID: runID, ProjectID: projectID}
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		return pkg, err
	}
	pkg.Repository = project.RepositoryPath
	var started, ended string
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(work_item_id,''),provider,COALESCE(model,''),COALESCE(task_type,''),state,started_at,COALESCE(ended_at,'') FROM runs WHERE id=? AND project_id=?`, runID, projectID).
		Scan(&pkg.WorkItemID, &pkg.Provider, &pkg.Model, &pkg.TaskType, &pkg.State, &started, &ended)
	if err != nil {
		return pkg, ErrNotFound
	}
	pkg.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	pkg.EndedAt, _ = time.Parse(time.RFC3339Nano, ended)
	if commit, commitErr := s.RunCommitByRun(ctx, runID); commitErr == nil {
		pkg.BaseCommit, pkg.Branch = commit.CommitSHA, commit.Branch
	} else if !errors.Is(commitErr, ErrNotFound) {
		return pkg, commitErr
	}
	if project.WorktreeEnabled && pkg.WorkItemID != "" {
		pkg.Worktree = project.RepositoryPath + ".goalforge-worktrees/" + pkg.WorkItemID
	}
	if prompt, promptErr := s.PromptForRun(ctx, runID); promptErr == nil {
		pkg.Prompt = prompt
	} else if !errors.Is(promptErr, ErrNotFound) {
		return pkg, promptErr
	}
	if pkg.Gates, err = s.ListGates(ctx, projectID); err != nil {
		return pkg, err
	}
	if pkg.Results, err = s.VerificationsForRun(ctx, runID); err != nil {
		return pkg, err
	}
	changes, err := s.ListRunFileChanges(ctx, runID)
	if err != nil {
		return pkg, err
	}
	for _, change := range changes {
		pkg.FileChanges = append(pkg.FileChanges, change.ChangeType+" "+change.Path)
	}
	if plan, planErr := s.RepairPlanForRun(ctx, runID); planErr == nil {
		pkg.Repair = plan
	} else if !errors.Is(planErr, ErrNotFound) {
		return pkg, planErr
	}
	return pkg, nil
}
