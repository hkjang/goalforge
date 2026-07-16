package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/goalforge/goalforge/internal/provider"
)

type Adapter struct {
	runner    *provider.ProcessRunner
	mu        sync.Mutex
	lastQuota *provider.QuotaSnapshot
}

func New(binary string) *Adapter {
	if binary == "" {
		binary = "claude"
	}
	return &Adapter{runner: provider.NewProcessRunner(binary)}
}
func (a *Adapter) Name() string { return "claude" }
func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true, SessionResume: true, RealtimeTokenUsage: true}
}
func (a *Adapter) Start(ctx context.Context, r provider.RunRequest) (<-chan provider.Event, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if r.Ephemeral {
		args = append(args, "--no-session-persistence")
	}
	if r.WorkspaceWrite {
		args = append(args, "--permission-mode", "acceptEdits")
	} else {
		args = append(args, "--permission-mode", "plan")
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	if r.OutputSchema != "" {
		args = append(args, "--json-schema", r.OutputSchema)
	}
	return a.runWithStopFailure(ctx, r, args)
}
func (a *Adapter) Resume(ctx context.Context, sessionID string, r provider.RunRequest) (<-chan provider.Event, error) {
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}
	args := []string{"-p", "--resume", sessionID, "--output-format", "stream-json", "--verbose"}
	if r.WorkspaceWrite {
		args = append(args, "--permission-mode", "acceptEdits")
	} else {
		args = append(args, "--permission-mode", "plan")
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	if r.OutputSchema != "" {
		args = append(args, "--json-schema", r.OutputSchema)
	}
	return a.runWithStopFailure(ctx, r, args)
}
func (a *Adapter) GetQuota(_ context.Context, _ provider.AccountRef) (provider.QuotaSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastQuota == nil {
		return provider.QuotaSnapshot{Provider: a.Name()}, provider.ErrQuotaUnavailable
	}
	result := *a.lastQuota
	if result.ResetAt != nil && !time.Now().UTC().Before(*result.ResetAt) {
		result.UsedPercent, result.LimitReached = 0, false
		result.Source, result.Confidence = "elapsed_reset_estimate", "medium"
	}
	return result, nil
}

func (a *Adapter) runWithStopFailure(ctx context.Context, request provider.RunRequest, args []string) (<-chan provider.Event, error) {
	request.Environment = append(request.Environment, claudeTelemetryEnvironment(request)...)
	directory, err := os.MkdirTemp("", "goalforge-claude-hook-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	capture := filepath.Join(directory, "stop-failure.jsonl")
	script := filepath.Join(directory, "capture-stop-failure.sh")
	settings := filepath.Join(directory, "settings.json")
	// The script runs under sh even on Windows (Claude Code ships with Git
	// Bash there), so paths are normalized to forward slashes and the hook
	// command uses double quotes, which cmd and sh both accept.
	capturePath := shellQuote(filepath.ToSlash(capture))
	scriptBody := "#!/bin/sh\numask 077\ncat >> " + capturePath + "\nprintf '\\n' >> " + capturePath + "\n"
	if err = os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		cleanup()
		return nil, err
	}
	hookCommand := "sh \"" + filepath.ToSlash(script) + "\""
	configuration := map[string]any{"hooks": map[string]any{"StopFailure": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": hookCommand, "timeout": 5}}}}}}
	rawSettings, _ := json.Marshal(configuration)
	if err = os.WriteFile(settings, rawSettings, 0o600); err != nil {
		cleanup()
		return nil, err
	}
	args = append(args, "--settings", settings, "--include-hook-events")
	source, err := a.runner.Run(ctx, request, args, DecodeLine)
	if err != nil {
		cleanup()
		return nil, err
	}
	out := make(chan provider.Event, 16)
	go func() {
		defer close(out)
		defer cleanup()
		for event := range source {
			if event.Type == provider.EventFailed && event.Message != "" {
				a.rememberQuota(ParseQuotaMessage(event.Message, time.Now()))
			}
			out <- event
		}
		file, openErr := os.Open(capture)
		if openErr != nil {
			if !errors.Is(openErr, os.ErrNotExist) {
				out <- provider.Event{Type: provider.EventFailed, RunID: request.RunID, Err: openErr}
			}
			return
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			failure, quota, decodeErr := DecodeStopFailure(line, time.Now())
			if decodeErr != nil {
				out <- provider.Event{Type: provider.EventFailed, RunID: request.RunID, Raw: line, Err: decodeErr}
				continue
			}
			a.rememberQuota(quota)
			out <- provider.Event{Type: provider.EventFailed, RunID: request.RunID, SessionID: failure.SessionID, Message: failure.Error + ": " + failure.LastAssistantMessage, Raw: line}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			out <- provider.Event{Type: provider.EventFailed, RunID: request.RunID, Err: scanErr}
		}
	}()
	return out, nil
}

func (a *Adapter) rememberQuota(snapshot provider.QuotaSnapshot) {
	if !snapshot.LimitReached {
		return
	}
	a.mu.Lock()
	a.lastQuota = &snapshot
	a.mu.Unlock()
}

func ParseQuotaMessage(message string, now time.Time) provider.QuotaSnapshot {
	lower := strings.ToLower(message)
	result := provider.QuotaSnapshot{Provider: "claude", LimitType: "session", Source: "cli_error", Confidence: "low", RawMessage: message}
	if !strings.Contains(lower, "rate limit") && !strings.Contains(lower, "usage limit") && !strings.Contains(lower, "session limit") && !strings.Contains(lower, "hit your") {
		return result
	}
	result.LimitReached, result.UsedPercent = true, 100
	if reset := ParseResetTime(message, now); reset != nil {
		result.ResetAt, result.Confidence = reset, "medium"
	}
	return result
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func claudeTelemetryEnvironment(request provider.RunRequest) []string {
	endpoint := os.Getenv("GOALFORGE_CLAUDE_OTEL_ENDPOINT")
	if endpoint == "" {
		return nil
	}
	protocol := os.Getenv("GOALFORGE_CLAUDE_OTEL_PROTOCOL")
	if protocol == "" {
		protocol = "http/protobuf"
	}
	attributes := "service.name=goalforge,goalforge.run.id=" + request.RunID
	return []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_METRICS_EXPORTER=otlp",
		"OTEL_LOGS_EXPORTER=otlp",
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + endpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL=" + protocol,
		"OTEL_RESOURCE_ATTRIBUTES=" + attributes,
		"OTEL_METRICS_INCLUDE_SESSION_ID=true",
		"OTEL_METRICS_INCLUDE_ACCOUNT_UUID=false",
	}
}
func (a *Adapter) Interrupt(ctx context.Context, runID string) error {
	return a.runner.Interrupt(ctx, runID)
}
