package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// deadSessionRetention is how long an invalidated session binding is kept.
// It is kept rather than deleted so the record of why a conversation was
// dropped survives the next run.
const deadSessionRetention = 30 * 24 * time.Hour

// handleDeadSessionFailure recovers a run that failed only because the session
// GoalForge asked to resume no longer exists on the provider's side.
//
// GoalForge stores a session ID and keeps handing it to --resume. The provider
// can discard that session at any time — expiry, a cleared cache, a different
// machine — and says so only when asked to resume it. The recovery path for
// this existed and was never reachable: provider.ErrSessionInvalid is checked
// on the synchronous return of Resume, but the provider reports the missing
// session asynchronously, in the event stream, after Resume has already
// returned nil. So the binding was retried until the project was marked FAILED
// and a person had to intervene, which is the outcome this tool exists to
// avoid.
//
// It returns handled=true when it has invalidated the binding, so the caller
// can run the turn again from a fresh session. The conversation is lost either
// way; what this changes is whether the goal stops.
func (o *Orchestrator) handleDeadSessionFailure(ctx context.Context, request Request, result Result, runErr error) bool {
	if !result.Resumed || result.SessionID == "" || runErr == nil {
		return false
	}
	if !provider.SessionGone(runErr.Error()) {
		return false
	}
	// Invalidating is what stops the next run reaching for the same dead
	// binding. Without it the recovery would work once and the run after it
	// would fail the same way.
	err := o.store.InvalidateSession(ctx, request.Project.ID, request.Project.Provider, result.SessionID,
		"제공자에 저장된 세션이 없어 재개할 수 없습니다", deadSessionRetention)
	// A binding another run already invalidated is the outcome this wanted, so
	// the race counts as success rather than failing one of the two runs that
	// discovered the same dead session.
	return err == nil || errors.Is(err, store.ErrNotFound)
}
