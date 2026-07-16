package qwen

import (
	"context"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/testscript"
)

func TestAdapterResumeInvocation(t *testing.T) {
	dir := t.TempDir()
	script := testscript.Write(t, dir, "qwen",
		"printf '{\"type\":\"system\",\"subtype\":\"session_start\",\"session_id\":\"%s\"}\\n' \"$*\"\ncat >/dev/null",
		"set args=%*\nset args=%args:\\=/%\necho {\"type\":\"system\",\"subtype\":\"session_start\",\"session_id\":\"%args%\"}\nmore > nul")
	a := New(script)
	events, err := a.Resume(context.Background(), "qwen-ses-1", provider.RunRequest{RunID: "run-1", Prompt: "continue", WorkDir: dir, Model: "qwen3-coder-plus", WorkspaceWrite: true})
	if err != nil {
		t.Fatal(err)
	}
	var session string
	for event := range events {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.SessionID != "" {
			session = event.SessionID
		}
	}
	if !strings.Contains(session, "--resume qwen-ses-1 --output-format stream-json --approval-mode auto-edit --model qwen3-coder-plus") {
		t.Fatalf("args=%q", session)
	}
}

func TestAdapterStartUsesPlanModeForReadOnly(t *testing.T) {
	dir := t.TempDir()
	script := testscript.Write(t, dir, "qwen",
		"printf '{\"type\":\"system\",\"subtype\":\"session_start\",\"session_id\":\"%s\"}\\n' \"$*\"\ncat >/dev/null",
		"set args=%*\nset args=%args:\\=/%\necho {\"type\":\"system\",\"subtype\":\"session_start\",\"session_id\":\"%args%\"}\nmore > nul")
	a := New(script)
	events, err := a.Start(context.Background(), provider.RunRequest{RunID: "run-2", Prompt: "inspect", WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var session string
	for event := range events {
		if event.SessionID != "" {
			session = event.SessionID
		}
	}
	if !strings.Contains(session, "--approval-mode plan") || strings.Contains(session, "auto-edit") {
		t.Fatalf("args=%q", session)
	}
}

func TestDecodeLineHandlesStreamAndBufferedShapes(t *testing.T) {
	events, err := DecodeLine([]byte(`{"type":"system","subtype":"session_start","session_id":"qs-1","model":"qwen3-coder-plus"}`))
	if err != nil || len(events) != 1 || events[0].Type != provider.EventSessionStarted || events[0].SessionID != "qs-1" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	events, err = DecodeLine([]byte(`{"type":"result","subtype":"success","session_id":"qs-1","is_error":false,"duration_ms":1234,"result":"done","usage":{"input_tokens":900,"output_tokens":210,"cache_read_input_tokens":300}}`))
	if err != nil || events[0].Type != provider.EventCompleted || events[0].Message != "done" || events[0].Usage == nil || events[0].Usage.InputTokens != 900 || events[0].Usage.CachedInputTokens != 300 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	events, err = DecodeLine([]byte(`{"type":"result","subtype":"error","session_id":"qs-1","is_error":true,"result":"quota exceeded"}`))
	if err != nil || events[0].Type != provider.EventFailed || events[0].Message != "quota exceeded" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	// The buffered --output-format json shape arrives as one JSON array.
	batch := `[{"type":"system","subtype":"session_start","session_id":"qs-2"},{"type":"assistant","session_id":"qs-2","message":{"content":[{"type":"text","text":"answer"}]}},{"type":"result","subtype":"success","session_id":"qs-2","is_error":false,"result":"answer"}]`
	events, err = DecodeLine([]byte(batch))
	if err != nil || len(events) != 3 || events[0].Type != provider.EventSessionStarted || events[1].Message != "answer" || events[2].Type != provider.EventCompleted {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
