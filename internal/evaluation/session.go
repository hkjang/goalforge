package evaluation

import (
	"context"

	"github.com/goalforge/goalforge/internal/model"
)

// Session is the part of a GoalForge run a trial needs. It is an interface so
// the runner can be exercised without a provider, and so the executor cannot
// quietly reach past it into state the trial is not supposed to see.
type Session interface {
	Continue(ctx context.Context, project model.Project) (Progress, error)
	Close()
}

// Progress is one work item's outcome inside a trial.
type Progress struct {
	Completed bool
	Tokens    int64
	CostUSD   float64
	RunID     string
	Detail    string
}

// SessionFactory builds a session against a trial's isolated state database
// and workspace.
type SessionFactory func(ctx context.Context, env Environment, project model.Project) (Session, error)
