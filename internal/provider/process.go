package provider

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
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
	cmd.Env = append(os.Environ(), request.Environment...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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
		if waitErr != nil && cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		if stdoutErr != nil {
			events <- Event{Type: EventFailed, RunID: request.RunID, Err: stdoutErr}
		}
		if stdinErr != nil {
			events <- Event{Type: EventFailed, RunID: request.RunID, Err: stdinErr}
		}
		if waitErr != nil {
			events <- Event{Type: EventFailed, RunID: request.RunID, Message: string(stderrBytes), Err: fmt.Errorf("%s exited: %w", r.Binary, waitErr)}
		}
	}()
	return events, nil
}

func (r *ProcessRunner) Interrupt(_ context.Context, runID string) error {
	r.mu.Lock()
	cmd := r.runs[runID]
	r.mu.Unlock()
	if cmd == nil {
		return fmt.Errorf("run %s: %w", runID, os.ErrNotExist)
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}
