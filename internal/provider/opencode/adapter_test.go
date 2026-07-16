package opencode

import (
	"context"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/testscript"
)

func fakeCLI(t *testing.T, dir string) string {
	t.Helper()
	return testscript.Write(t, dir, "opencode",
		"printf '{\"type\":\"step_finish\",\"sessionID\":\"%s\"}\\n' \"$*\"\ncat >/dev/null",
		"set args=%*\nset args=%args:\\=/%\necho {\"type\":\"step_finish\",\"sessionID\":\"%args%\"}\nmore > nul")
}

func TestAdapterResumeInvocation(t *testing.T) {
	dir := t.TempDir()
	a := New(fakeCLI(t, dir))
	events, err := a.Resume(context.Background(), "ses_abc", provider.RunRequest{RunID: "run-1", Prompt: "continue", WorkDir: dir, Model: "anthropic/claude-sonnet-4-6", WorkspaceWrite: true})
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
	if !strings.Contains(session, "run --format json --auto --model anthropic/claude-sonnet-4-6 --session ses_abc") {
		t.Fatalf("args=%q", session)
	}
}

func TestAdapterStartUsesPlanAgentForReadOnly(t *testing.T) {
	dir := t.TempDir()
	a := New(fakeCLI(t, dir))
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
	if !strings.Contains(session, "--agent plan") || strings.Contains(session, "--auto ") {
		t.Fatalf("args=%q", session)
	}
}

func TestDecodeLineTolerantShapes(t *testing.T) {
	events, err := DecodeLine([]byte(`{"type":"text","sessionID":"ses_1","text":"working on it"}`))
	if err != nil || events[0].Type != provider.EventMessage || events[0].Message != "working on it" || events[0].SessionID != "ses_1" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	events, err = DecodeLine([]byte(`{"type":"step_finish","sessionID":"ses_1","tokens":{"input":1500,"output":420,"reasoning":50,"cache":{"read":700,"write":80}}}`))
	if err != nil || events[0].Type != provider.EventCompleted || events[0].Usage == nil || events[0].Usage.InputTokens != 1500 || events[0].Usage.CachedInputTokens != 700 || events[0].Usage.CacheCreationTokens != 80 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	events, err = DecodeLine([]byte(`{"type":"message.part.updated","part":{"sessionID":"ses_2","type":"text","text":"partial"}}`))
	if err != nil || events[0].SessionID != "ses_2" || events[0].Message != "partial" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	events, err = DecodeLine([]byte(`{"type":"error","error":{"name":"ProviderAuthError","data":{"message":"401 unauthorized"}}}`))
	if err != nil || events[0].Type != provider.EventFailed || events[0].Message != "401 unauthorized" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
