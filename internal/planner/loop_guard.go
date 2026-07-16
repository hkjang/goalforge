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

// LoopAction tells the caller how to break a detected loop.
type LoopAction string

const (
	LoopNone LoopAction = "NONE"
	// LoopRotateSession asks for a fresh provider session (LOOP-005: repeated
	// completion claims without file changes replace the session first).
	LoopRotateSession LoopAction = "ROTATE_SESSION"
	// LoopBlock means the project was blocked for user review.
	LoopBlock LoopAction = "BLOCK"
)

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
func (g *LoopGuard) Record(ctx context.Context, projectID, workID, signal, fingerprint, runID string) (LoopAction, int, error) {
	count, err := g.store.RecordLoopSignal(ctx, projectID, workID, signal, fingerprint, runID)
	if err != nil {
		return LoopNone, 0, err
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
		return LoopNone, count, errors.New("unknown loop signal")
	}
	if limit <= 0 {
		return LoopNone, count, nil
	}
	if signal == "no_change" {
		// Replace the session at the threshold; block only when a fresh
		// session keeps claiming completion without changing anything.
		if count >= 2*limit {
			return LoopBlock, count, g.store.BlockProjectForLoop(ctx, projectID)
		}
		if count >= limit {
			return LoopRotateSession, count, nil
		}
		return LoopNone, count, nil
	}
	if count >= limit {
		return LoopBlock, count, g.store.BlockProjectForLoop(ctx, projectID)
	}
	return LoopNone, count, nil
}
