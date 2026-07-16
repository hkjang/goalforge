package planner

import (
	"context"
	"errors"

	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

type LoopPolicy struct{ SameError, NoChange, SameWork, SameChange int }

func DefaultLoopPolicy() LoopPolicy {
	return LoopPolicy{SameError: 3, NoChange: 2, SameWork: 3, SameChange: 3}
}

type LoopGuard struct {
	store  *store.Store
	policy LoopPolicy
}

func NewLoopGuard(s *store.Store, p LoopPolicy) (*LoopGuard, error) {
	if s == nil {
		return nil, errors.New("store is required")
	}
	return &LoopGuard{store: s, policy: p}, nil
}
func (g *LoopGuard) Record(ctx context.Context, projectID, workID, signal, fingerprint, runID string) (bool, int, error) {
	count, err := g.store.RecordLoopSignal(ctx, projectID, workID, signal, fingerprint, runID)
	if err != nil {
		return false, 0, err
	}
	limit := 0
	switch signal {
	case "same_error":
		limit = g.policy.SameError
	case "no_change":
		limit = g.policy.NoChange
	case "same_work":
		limit = g.policy.SameWork
	case "same_change":
		limit = g.policy.SameChange
	default:
		return false, count, errors.New("unknown loop signal")
	}
	blocked := limit > 0 && count >= limit
	if blocked {
		if err := g.store.BlockProjectForLoop(ctx, projectID); err != nil {
			return false, count, err
		}
	}
	return blocked, count, nil
}
