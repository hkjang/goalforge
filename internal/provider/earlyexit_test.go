package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"

	"github.com/goalforge/goalforge/internal/testscript"
)

func collect(t *testing.T, script, prompt string, dir string) []Event {
	t.Helper()
	runner := NewProcessRunner(script)
	events, err := runner.Run(context.Background(),
		RunRequest{RunID: "run-1", Prompt: prompt, WorkDir: dir}, nil,
		func(line []byte) ([]Event, error) {
			return []Event{{Type: EventMessage, Message: string(line)}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	var got []Event
	for event := range events {
		got = append(got, event)
	}
	return got
}

// A provider that did its work and exited without draining the prompt makes the
// write fail with a broken pipe. That is not a failure of the run — the provider
// finished — but it was reported as one, so a run that succeeded came back as
// `error: write |1: broken pipe`.
//
// Worse, it depends on timing: the same provider reports success or failure
// depending on whether the write lost the race with the exit.
func TestAProviderThatExitsBeforeReadingThePromptHasNotFailed(t *testing.T) {
	dir := t.TempDir()
	// Prints its result and exits without reading stdin. The prompt is large
	// enough that the write cannot complete into the pipe buffer first.
	script := testscript.Write(t, dir, "early-exit",
		"echo '{\"type\":\"done\"}'\nexit 0",
		"echo {\"type\":\"done\"}")
	got := collect(t, script, strings.Repeat("x", 1<<20), dir)
	for _, event := range got {
		if event.Type == EventFailed {
			t.Fatalf("the provider exited successfully: %+v", event)
		}
	}
	if len(got) == 0 {
		t.Fatal("its output must still be delivered")
	}
}

// When the provider fails, what explains it is its own stderr and its exit
// status — not the pipe error that followed from it. Reporting the pipe error
// sends the reader to look at GoalForge's plumbing for a provider that could
// not authenticate.
func TestAFailingProviderIsReportedByItsStderrNotItsPipe(t *testing.T) {
	dir := t.TempDir()
	script := testscript.Write(t, dir, "auth-fail",
		"echo 'error: not logged in' >&2\nexit 1",
		"echo error: not logged in 1>&2\nexit /b 1")
	got := collect(t, script, strings.Repeat("x", 1<<20), dir)
	var failures []Event
	for _, event := range got {
		if event.Type == EventFailed {
			failures = append(failures, event)
		}
	}
	if len(failures) != 1 {
		t.Fatalf("one failure, naming the exit: %+v", failures)
	}
	if !strings.Contains(failures[0].Message, "not logged in") {
		t.Fatalf("the provider's own words are the explanation: %+v", failures[0])
	}
	if failures[0].Err == nil || !strings.Contains(failures[0].Err.Error(), "exited") {
		t.Fatalf("and the exit status: %+v", failures[0])
	}
	for _, failure := range failures {
		if failure.Err != nil && strings.Contains(failure.Err.Error(), "broken pipe") {
			t.Fatalf("the pipe error followed from the exit; it does not explain it: %+v", failure)
		}
	}
}

// A provider that reads the prompt and then fails is unchanged: there is no
// pipe error to suppress, and the exit is still reported.
func TestAProviderThatReadsThenFailsIsStillReported(t *testing.T) {
	dir := t.TempDir()
	script := testscript.Write(t, dir, "read-then-fail",
		"cat >/dev/null\necho 'boom' >&2\nexit 2",
		"set /p x=\necho boom 1>&2\nexit /b 2")
	got := collect(t, script, "prompt\n", dir)
	var failures int
	for _, event := range got {
		if event.Type == EventFailed {
			failures++
			if !strings.Contains(event.Message, "boom") {
				t.Fatalf("stderr must be attached: %+v", event)
			}
		}
	}
	if failures != 1 {
		t.Fatalf("failures=%d", failures)
	}
}

// isBrokenPipe decides whether a write failure is the provider having stopped
// reading. Classifying anything else that way would swallow a real write
// failure, and the run would come back succeeded with the prompt never
// delivered.
//
// The branch that reports a non-pipe write error is defensive: on an os/exec
// stdin pipe the realistic failures are EPIPE and a pipe torn down after the
// process exited, and a test cannot produce another one. What can be pinned is
// that this does not claim errors it has not seen.
func TestOnlyAPipeFailureCountsAsTheProviderHavingStopped(t *testing.T) {
	pipe := []error{
		syscall.EPIPE,
		io.ErrClosedPipe,
		fmt.Errorf("write |1: %w", syscall.EPIPE),
		errors.New("write |1: broken pipe"),
		errors.New("file already closed"),
	}
	for _, err := range pipe {
		if !isBrokenPipe(err) {
			t.Fatalf("the provider stopped reading: %v", err)
		}
	}
	other := []error{
		errors.New("input/output error"),
		syscall.ENOSPC,
		errors.New("context deadline exceeded"),
		fmt.Errorf("encode prompt: %w", errors.New("invalid utf-8")),
	}
	for _, err := range other {
		if isBrokenPipe(err) {
			t.Fatalf("that is a write that failed, not a reader that left: %v", err)
		}
	}
}
