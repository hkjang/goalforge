// Package qwen adapts Qwen Code (an open-source terminal coding agent) to
// the GoalForge provider interface. Qwen Code's headless mode emits a
// Claude-Code-compatible stream-json event stream and resumes sessions with
// --resume, so the adapter mirrors the Claude CLI mapping.
package qwen

import (
	"context"
	"errors"

	"github.com/goalforge/goalforge/internal/provider"
)

type Adapter struct{ runner *provider.ProcessRunner }

func New(binary string) *Adapter {
	if binary == "" {
		binary = "qwen"
	}
	return &Adapter{runner: provider.NewProcessRunner(binary)}
}
func (a *Adapter) Name() string { return "qwen" }
func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true, RealtimeTokenUsage: true}
}

// baseArgs maps the run request onto Qwen Code headless flags. Writable work
// uses auto-edit (edits approved, shell still gated) rather than yolo: broad
// permission modes must never be the default (SEC guidance).
func baseArgs(r provider.RunRequest) []string {
	args := []string{"--output-format", "stream-json"}
	if r.WorkspaceWrite {
		args = append(args, "--approval-mode", "auto-edit")
	} else {
		args = append(args, "--approval-mode", "plan")
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
	args := append([]string{"--resume", sessionID}, baseArgs(r)...)
	return a.runner.Run(ctx, r, args, DecodeLine)
}

func (a *Adapter) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	return provider.QuotaSnapshot{Provider: a.Name()}, provider.ErrQuotaUnavailable
}
func (a *Adapter) Interrupt(ctx context.Context, runID string) error {
	return a.runner.Interrupt(ctx, runID)
}

var _ provider.Provider = (*Adapter)(nil)
