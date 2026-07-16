// Package opencode adapts the OpenCode terminal coding agent to the
// GoalForge provider interface via `opencode run --format json`, which emits
// newline-delimited JSON events. Sessions resume with --session, read-only
// work runs under the built-in plan agent, and writable work auto-approves
// only permissions the user has not explicitly denied (--auto) — never the
// dangerously-skip-permissions mode.
package opencode

import (
	"context"
	"errors"

	"github.com/goalforge/goalforge/internal/provider"
)

type Adapter struct{ runner *provider.ProcessRunner }

func New(binary string) *Adapter {
	if binary == "" {
		binary = "opencode"
	}
	return &Adapter{runner: provider.NewProcessRunner(binary)}
}
func (a *Adapter) Name() string { return "opencode" }
func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true, RealtimeTokenUsage: true}
}

func baseArgs(r provider.RunRequest) []string {
	args := []string{"run", "--format", "json"}
	if r.WorkspaceWrite {
		args = append(args, "--auto")
	} else {
		args = append(args, "--agent", "plan")
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	return args
}

func (a *Adapter) Start(ctx context.Context, r provider.RunRequest) (<-chan provider.Event, error) {
	return a.runner.Run(ctx, r, baseArgs(r), DecodeLine)
}

func (a *Adapter) Resume(ctx context.Context, sessionID string, r provider.RunRequest) (<-chan provider.Event, error) {
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}
	args := append(baseArgs(r), "--session", sessionID)
	return a.runner.Run(ctx, r, args, DecodeLine)
}

func (a *Adapter) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	return provider.QuotaSnapshot{Provider: a.Name()}, provider.ErrQuotaUnavailable
}
func (a *Adapter) Interrupt(ctx context.Context, runID string) error {
	return a.runner.Interrupt(ctx, runID)
}

var _ provider.Provider = (*Adapter)(nil)
