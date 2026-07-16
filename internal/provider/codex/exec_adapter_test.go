package codex

import (
	"context"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/testscript"
)

func TestAdapterResumeInvocation(t *testing.T) {
	dir := t.TempDir()
	script := testscript.Write(t, dir, "codex",
		"printf '{\"type\":\"thread.started\",\"thread_id\":\"%s\"}\\n' \"$*\"\ncat >/dev/null",
		"set args=%*\nset args=%args:\\=/%\necho {\"type\":\"thread.started\",\"thread_id\":\"%args%\"}\nmore > nul")
	a := New(script)
	events, err := a.Resume(context.Background(), "thr_old", provider.RunRequest{RunID: "run-1", Prompt: "continue", WorkDir: dir, Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	var session string
	for event := range events {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		session = event.SessionID
	}
	if !strings.Contains(session, "exec resume --json --sandbox read-only --model gpt-test thr_old -") {
		t.Fatalf("args=%q", session)
	}
}
