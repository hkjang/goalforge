// Package qwen adapts Qwen Code (an open-source terminal coding agent) to
// the GoalForge provider interface. Qwen Code's headless mode emits a
// Claude-Code-compatible stream-json event stream and resumes sessions with
// --resume, so the adapter mirrors the Claude CLI mapping.
package qwen

import (
	"context"
	"errors"
	"strings"

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
//
// auto-edit leaves shell gated, and a headless session has nobody to ask — so
// the commands the project declared as its gates are passed as an allowlist.
// Without it a session cannot run the project's own tests to check its work: it
// writes code, reports done, and the gates find out afterwards.
func baseArgs(r provider.RunRequest) []string {
	args := []string{"--output-format", "stream-json"}
	if r.WorkspaceWrite {
		args = append(args, "--approval-mode", "auto-edit")
		// Only on a writable run. The point of plan mode is that the session
		// looks and does not act, and pre-approving shell commands there would
		// make the mode a label.
		if patterns := shellToolPatterns(r.AllowedCommands); len(patterns) > 0 {
			args = append(args, "--allowed-tools", strings.Join(patterns, ","))
		}
	} else {
		args = append(args, "--approval-mode", "plan")
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	return args
}

// shellToolPatterns renders commands in the form Qwen Code matches on.
//
// Qwen compares the invoked command against the text between the first "(" and
// a trailing ")": `value === pattern || value.startsWith(pattern + " ")`. So
// run_shell_command(go test) admits `go test` and `go test ./pkg` and nothing
// else, and a chained command is split with each part checked separately —
// `go test ./... ; rm -rf /` still stops at the second part.
//
// A command carrying a parenthesis cannot be expressed in that form, so it is
// dropped rather than passed: a malformed pattern matches nothing, which looks
// exactly like an allowlist the tool ignored.
func shellToolPatterns(commands []string) []string {
	patterns := make([]string, 0, len(commands))
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" || strings.ContainsAny(command, "(),") {
			continue
		}
		patterns = append(patterns, "run_shell_command("+command+")")
	}
	return patterns
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
