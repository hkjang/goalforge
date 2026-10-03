package provider

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/goalforge/goalforge/internal/policy"

	"github.com/goalforge/goalforge/internal/procctl"
)

type DecodeLine func([]byte) ([]Event, error)

type ProcessRunner struct {
	Binary string
	mu     sync.Mutex
	runs   map[string]*exec.Cmd
}

func NewProcessRunner(binary string) *ProcessRunner {
	return &ProcessRunner{Binary: binary, runs: make(map[string]*exec.Cmd)}
}

func (r *ProcessRunner) Run(ctx context.Context, request RunRequest, args []string, decode DecodeLine) (<-chan Event, error) {
	if request.RunID == "" {
		return nil, errors.New("run ID is required")
	}
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Dir = request.WorkDir
	// An implementation session gets a named environment and the role marker,
	// not whatever the operator happened to export: unrelated credentials are
	// not something to hand over just because they were in scope.
	cmd.Env = append(policy.SessionEnvironment(os.Environ(), policy.RoleImplementation), request.Environment...)
	procctl.SetGroup(cmd)
	cmd.Cancel = func() error {
		return procctl.KillGroup(cmd)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", r.Binary, err)
	}
	r.mu.Lock()
	if _, exists := r.runs[request.RunID]; exists {
		r.mu.Unlock()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("run %s already exists", request.RunID)
	}
	r.runs[request.RunID] = cmd
	r.mu.Unlock()
	events := make(chan Event, 16)
	go func() {
		defer close(events)
		defer func() { r.mu.Lock(); delete(r.runs, request.RunID); r.mu.Unlock() }()
		writeErr := make(chan error, 1)
		go func() {
			_, e := io.WriteString(stdin, request.Prompt)
			if e == nil {
				e = stdin.Close()
			}
			writeErr <- e
		}()
		scanErr := make(chan error, 1)
		go func() {
			scanner := bufio.NewScanner(stdout)
			scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
			for scanner.Scan() {
				decoded, e := decode(append([]byte(nil), scanner.Bytes()...))
				if e != nil {
					events <- Event{Type: EventFailed, RunID: request.RunID, Raw: append([]byte(nil), scanner.Bytes()...), Err: e}
					continue
				}
				for _, event := range decoded {
					event.RunID = request.RunID
					events <- event
				}
			}
			scanErr <- scanner.Err()
		}()
		stderrBytes, _ := io.ReadAll(stderr)
		stdoutErr := <-scanErr
		stdinErr := <-writeErr
		waitErr := cmd.Wait()
		if waitErr != nil {
			_ = procctl.KillGroup(cmd)
		}
		if stdoutErr != nil {
			events <- Event{Type: EventFailed, RunID: request.RunID, Err: stdoutErr}
		}
		// A broken pipe means the provider stopped reading, which is a
		// consequence rather than a cause: either it finished its work and
		// exited — in which case nothing failed — or it died, and its exit
		// status and stderr below are what explain that. Reported on its own
		// it turned a successful run into `write |1: broken pipe`, and it
		// depended on whether the write lost the race with the exit, so the
		// same provider came back succeeded or failed.
		//
		// The non-pipe branch is defensive and untested: on an os/exec stdin
		// pipe the realistic failures are EPIPE and a pipe torn down after the
		// process exited, and a test cannot produce another one. What is
		// tested is that isBrokenPipe does not claim errors it has not seen.
		if stdinErr != nil && !isBrokenPipe(stdinErr) {
			events <- Event{Type: EventFailed, RunID: request.RunID, Err: stdinErr}
		}
		if waitErr != nil {
			events <- Event{Type: EventFailed, RunID: request.RunID, Message: string(stderrBytes), Err: fmt.Errorf("%s exited: %w", r.Binary, waitErr)}
		}
	}()
	return events, nil
}

// isBrokenPipe reports whether a write failed because the other end is gone.
//
// EPIPE is what the kernel gives a write to a pipe nobody is reading;
// ErrClosedPipe is what os/exec gives once it has torn the pipe down after the
// process exited. Both mean the same thing here, and the string is checked as
// well because a write wrapped on the way up loses the sentinel.
func isBrokenPipe(err error) bool {
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	return strings.Contains(err.Error(), "broken pipe") ||
		strings.Contains(err.Error(), "file already closed")
}

func (r *ProcessRunner) Interrupt(_ context.Context, runID string) error {
	r.mu.Lock()
	cmd := r.runs[runID]
	r.mu.Unlock()
	if cmd == nil {
		return fmt.Errorf("run %s: %w", runID, os.ErrNotExist)
	}
	return procctl.InterruptGroup(cmd)
}
