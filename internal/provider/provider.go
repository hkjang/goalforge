package provider

import (
	"context"
	"encoding/json"
	"time"
)

type Provider interface {
	Name() string
	Capabilities() Capabilities
	Start(context.Context, RunRequest) (<-chan Event, error)
	Resume(context.Context, string, RunRequest) (<-chan Event, error)
	GetQuota(context.Context, AccountRef) (QuotaSnapshot, error)
	Interrupt(context.Context, string) error
}

type Capabilities struct {
	StructuredStream, SessionResume, ExactQuotaResetAt bool
	RealtimeTokenUsage, NativeGoal, NativeTokenBudget  bool
}

type RunRequest struct {
	RunID, Prompt, WorkDir, Model string
	OutputSchema                  string
	GoalObjective                 string
	GoalTokenBudget               *int64
	Environment                   []string
	// AllowedCommands are shell commands the session may run without being
	// asked to confirm them.
	//
	// A headless session has nobody to ask. An agent that gates shell commands
	// — Qwen Code does, even with edits auto-approved — cannot run the
	// project's own tests to check its work, so it writes code, reports done,
	// and the gates find out afterwards.
	//
	// They are derived from the project's verification gates rather than
	// configured, so allowing them grants no authority the run did not already
	// have: those commands are going to run anyway, as the gates. Adapters that
	// cannot express a command allowlist ignore this.
	AllowedCommands []string
	WorkspaceWrite  bool
	Ephemeral       bool
}

type AccountRef struct{ ID string }

type QuotaSnapshot struct {
	Provider, LimitType, Source, Confidence, RawMessage string
	UsedPercent                                         float64
	Remaining                                           *float64
	ResetAt                                             *time.Time
	RetryAfter                                          *time.Duration
	LimitReached                                        bool
}

type Usage struct {
	InputTokens, OutputTokens, CachedInputTokens int64
	CacheCreationTokens, ReasoningTokens         int64
	CostUSD                                      float64
}

type Event struct {
	Type, RunID, SessionID, TurnID, Message string
	Usage                                   *Usage
	Raw                                     json.RawMessage
	Err                                     error
}

const (
	EventSessionStarted = "session.started"
	EventTurnStarted    = "turn.started"
	EventUsage          = "usage"
	EventMessage        = "message"
	EventCompleted      = "completed"
	EventFailed         = "failed"
)

var ErrQuotaUnavailable = &providerError{"quota information unavailable through CLI adapter"}
var ErrSessionInvalid = &providerError{"provider session is invalid or unavailable"}

type providerError struct{ message string }

func (e *providerError) Error() string { return e.message }
