package verification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
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
	// ValuePattern is a regular expression with one capture group that
	// extracts the measured value from the gate's output. Without it a
	// passing gate can only record its own configured SuccessValue, which
	// proves the command exited zero but never proves a number such as
	// coverage or latency.
	ValuePattern string
}
type Result struct {
	Type, Status, Output string
	// FailureKind, RepairMode, and FailureSummary explain a failure instead of
	// leaving the caller to read the output and guess.
	FailureKind, RepairMode, FailureSummary string
	ExitCode                                int
	Duration                                time.Duration
	Required                                bool
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

// Check executes gates against a working tree and measures their results
// without recording anything. Integration verification uses it to test the
// merged result on the default branch, which belongs to no single run: two
// work items that each verified in their own worktree say nothing about
// whether their combination works.
func (e *Engine) Check(ctx context.Context, repositoryPath string, gates []Gate) ([]Result, bool, error) {
	if len(gates) == 0 {
		return nil, false, errors.New("at least one verification gate is required")
	}
	results := make([]Result, 0, len(gates))
	passed := true
	for _, gate := range gates {
		result, err := e.runGate(ctx, repositoryPath, gate)
		measure(gate, &result)
		if result.Status != "PASSED" {
			failure := policy.ClassifyGateFailure(result.Status, result.Output)
			result.FailureKind, result.RepairMode, result.FailureSummary = string(failure.Kind), string(failure.Mode), failure.Summary
		}
		results = append(results, result)
		if gate.Required && result.Status != "PASSED" {
			passed = false
		}
		if err != nil && ctx.Err() != nil {
			return results, false, err
		}
	}
	return results, passed, nil
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
		actual := measure(gate, &result)
		record := store.VerificationRecord{RunID: runID, CheckType: gate.Type, Status: result.Status, ActualValue: actual,
			Command: strings.Join(gate.Command, " "), Output: result.Output, ExitCode: result.ExitCode,
			Duration: result.Duration, Required: gate.Required}
		if result.Status != "PASSED" {
			failure := policy.ClassifyGateFailure(result.Status, result.Output)
			record.FailureKind, record.RepairMode = string(failure.Kind), string(failure.Mode)
			result.FailureKind, result.RepairMode, result.FailureSummary = string(failure.Kind), string(failure.Mode), failure.Summary
		}
		report.Results = append(report.Results, result)
		recordErr := e.store.RecordRunVerification(ctx, record)
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

// measure derives the value recorded as evidence for a gate. A gate without
// a value pattern keeps its boolean meaning. A gate with one must produce a
// measurement that clears SuccessValue: a command that exits zero while
// reporting 71% coverage against an 85% threshold is a failure, and an
// unparseable output is not evidence, so neither is allowed to pass.
func measure(gate Gate, result *Result) string {
	if result.Status != "PASSED" {
		return "false"
	}
	if gate.ValuePattern == "" {
		if gate.SuccessValue == "" {
			return "true"
		}
		return gate.SuccessValue
	}
	pattern, err := regexp.Compile(gate.ValuePattern)
	if err != nil {
		result.Status = "FAILED"
		result.Output += fmt.Sprintf("\n[gate %s: value pattern is invalid: %v]", gate.Type, err)
		return ""
	}
	match := pattern.FindStringSubmatch(result.Output)
	if len(match) < 2 {
		result.Status = "FAILED"
		result.Output += fmt.Sprintf("\n[gate %s: value pattern matched no measurement in the output]", gate.Type)
		return ""
	}
	actual := strings.TrimSpace(match[1])
	threshold, thresholdErr := strconv.ParseFloat(gate.SuccessValue, 64)
	if thresholdErr == nil {
		measured, measuredErr := strconv.ParseFloat(actual, 64)
		if measuredErr != nil {
			result.Status = "FAILED"
			result.Output += fmt.Sprintf("\n[gate %s: measured %q is not a number]", gate.Type, actual)
			return actual
		}
		if measured < threshold {
			result.Status = "FAILED"
			result.Output += fmt.Sprintf("\n[gate %s: measured %s is below the threshold %s]", gate.Type, actual, gate.SuccessValue)
		}
		return actual
	}
	if gate.SuccessValue != "" && actual != gate.SuccessValue {
		result.Status = "FAILED"
		result.Output += fmt.Sprintf("\n[gate %s: measured %q does not equal the required %q]", gate.Type, actual, gate.SuccessValue)
	}
	return actual
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
