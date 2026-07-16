package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
)

func TestAdapterResumeInvocation(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	body := "#!/bin/sh\nprintf '{\"type\":\"thread.started\",\"thread_id\":\"%s\"}\\n' \"$*\"\ncat >/dev/null\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
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
