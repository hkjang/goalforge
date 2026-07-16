package verification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/policy"
	"github.com/goalforge/goalforge/internal/procctl"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

type Gate struct {
	Type         string
	Command      []string
	Timeout      time.Duration
	Required     bool
	SuccessValue string
}
type Result struct {
	Type, Status, Output string
	ExitCode             int
	Duration             time.Duration
	Required             bool
}
type Report struct {
	Results               []Result
	Passed, GoalCompleted bool
	Progress              float64
}
type Engine struct {
	store          *store.Store
	maxOutputBytes int
}

func New(s *store.Store, maxOutputBytes int) (*Engine, error) {
	if s == nil || maxOutputBytes <= 0 {
		return nil, errors.New("store and positive output limit are required")
	}
	return &Engine{store: s, maxOutputBytes: maxOutputBytes}, nil
}

func (e *Engine) Verify(ctx context.Context, runID string, project model.Project, gates []Gate) (Report, error) {
	report := Report{Passed: true}
	if len(gates) == 0 {
		return report, errors.New("at least one verification gate is required")
	}
	requiredCount := 0
	for _, gate := range gates {
		if gate.Required {
			requiredCount++
		}
	}
	if requiredCount == 0 {
		return report, errors.New("at least one required verification gate is required")
	}
	for _, gate := range gates {
		result, err := e.runGate(ctx, project.RepositoryPath, gate)
		report.Results = append(report.Results, result)
		actual := "false"
		if result.Status == "PASSED" {
			actual = gate.SuccessValue
			if actual == "" {
				actual = "true"
			}
		}
		recordErr := e.store.RecordRunVerification(ctx, store.VerificationRecord{RunID: runID, CheckType: gate.Type, Status: result.Status, ActualValue: actual, Command: strings.Join(gate.Command, " "), Output: result.Output, ExitCode: result.ExitCode, Duration: result.Duration, Required: gate.Required})
		if recordErr != nil {
			return report, recordErr
		}
		if gate.Required && result.Status != "PASSED" {
			report.Passed = false
		}
		if err != nil && ctx.Err() != nil {
			return report, err
		}
	}
	goal, err := e.store.ApplyVerificationOutcome(ctx, runID, report.Passed)
	if err != nil {
		return report, err
	}
	if !report.Passed {
		return report, nil
	}
	report.Progress, report.GoalCompleted, err = e.store.GoalProgress(ctx, goal)
	if err != nil {
		return report, err
	}
	if err = e.store.FinalizeCheckpoint(ctx, project.ID, goal.ID, report.GoalCompleted); err != nil {
		return report, err
	}
	return report, nil
}

func (e *Engine) runGate(parent context.Context, workDir string, gate Gate) (Result, error) {
	result := Result{Type: gate.Type, Status: "FAILED", ExitCode: -1, Required: gate.Required}
	if gate.Type == "" || len(gate.Command) == 0 {
		return result, errors.New("gate type and command are required")
	}
	if gate.Timeout <= 0 {
		return result, errors.New("gate timeout must be positive")
	}
	if err := policy.ValidateCommand(gate.Command); err != nil {
		return result, fmt.Errorf("verification command blocked by policy: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, gate.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gate.Command[0], gate.Command[1:]...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	procctl.SetGroup(cmd)
	cmd.Cancel = func() error {
		return procctl.KillGroup(cmd)
	}
	writer := &limitWriter{remaining: e.maxOutputBytes}
	cmd.Stdout = writer
	cmd.Stderr = writer
	started := time.Now()
	err := cmd.Start()
	if err == nil {
		err = cmd.Wait()
	}
	result.Duration = time.Since(started)
	result.Output = writer.String()
	if err == nil {
		result.Status = "PASSED"
		result.ExitCode = 0
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Status = "TIMEOUT"
		return result, fmt.Errorf("gate %s timed out: %w", gate.Type, ctx.Err())
	}
	return result, err
}

type limitWriter struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	original := len(p)
	if w.remaining <= 0 {
		w.truncated = true
		return original, nil
	}
	chunk := p
	if len(chunk) > w.remaining {
		chunk = chunk[:w.remaining]
		w.truncated = true
	}
	_, _ = w.buffer.Write(chunk)
	w.remaining -= len(chunk)
	return original, nil
}
func (w *limitWriter) String() string {
	if w.truncated {
		return w.buffer.String() + "\n[output truncated]"
	}
	return w.buffer.String()
}

var _ io.Writer = (*limitWriter)(nil)
