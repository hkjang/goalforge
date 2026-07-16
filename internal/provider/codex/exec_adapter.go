package codex

import (
	"context"
	"errors"
	"os"

	"github.com/goalforge/goalforge/internal/provider"
)

type Adapter struct{ runner *provider.ProcessRunner }

func New(binary string) *Adapter {
	if binary == "" {
		binary = "codex"
	}
	return &Adapter{runner: provider.NewProcessRunner(binary)}
}
func (a *Adapter) Name() string { return "codex" }
func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true, RealtimeTokenUsage: true}
}
func (a *Adapter) Start(ctx context.Context, r provider.RunRequest) (<-chan provider.Event, error) {
	args := []string{"exec", "--json"}
	if r.Ephemeral {
		args = append(args, "--ephemeral")
	}
	if r.WorkspaceWrite {
		args = append(args, "--sandbox", "workspace-write")
	} else {
		args = append(args, "--sandbox", "read-only")
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	args, cleanup, err := addOutputSchema(args, r.OutputSchema)
	if err != nil {
		return nil, err
	}
	args = append(args, "-")
	return runAndCleanup(a.runner, ctx, r, args, cleanup)
}
func (a *Adapter) Resume(ctx context.Context, sessionID string, r provider.RunRequest) (<-chan provider.Event, error) {
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}
	args := []string{"exec", "resume", "--json"}
	if r.WorkspaceWrite {
		args = append(args, "--sandbox", "workspace-write")
	} else {
		args = append(args, "--sandbox", "read-only")
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	args, cleanup, err := addOutputSchema(args, r.OutputSchema)
	if err != nil {
		return nil, err
	}
	args = append(args, sessionID, "-")
	return runAndCleanup(a.runner, ctx, r, args, cleanup)
}

func addOutputSchema(args []string, schema string) ([]string, func(), error) {
	if schema == "" {
		return args, func() {}, nil
	}
	f, err := os.CreateTemp("", "goalforge-schema-*.json")
	if err != nil {
		return nil, nil, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err = f.WriteString(schema); err != nil {
		_ = f.Close()
		cleanup()
		return nil, nil, err
	}
	if err = f.Close(); err != nil {
		cleanup()
		return nil, nil, err
	}
	return append(args, "--output-schema", path), cleanup, nil
}

func runAndCleanup(runner *provider.ProcessRunner, ctx context.Context, request provider.RunRequest, args []string, cleanup func()) (<-chan provider.Event, error) {
	source, err := runner.Run(ctx, request, args, DecodeLine)
	if err != nil {
		cleanup()
		return nil, err
	}
	out := make(chan provider.Event)
	go func() {
		defer close(out)
		defer cleanup()
		for event := range source {
			out <- event
		}
	}()
	return out, nil
}
func (a *Adapter) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	return provider.QuotaSnapshot{Provider: a.Name()}, provider.ErrQuotaUnavailable
}
func (a *Adapter) Interrupt(ctx context.Context, runID string) error {
	return a.runner.Interrupt(ctx, runID)
}
