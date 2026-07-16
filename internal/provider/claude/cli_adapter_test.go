package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/testscript"
)

func TestAdapterResumeInvocation(t *testing.T) {
	dir := t.TempDir()
	script := testscript.Write(t, dir, "claude",
		"printf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"%s\"}\\n' \"$*\"\ncat >/dev/null",
		"set args=%*\nset args=%args:\\=/%\necho {\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"%args%\"}\nmore > nul")
	a := New(script)
	events, err := a.Resume(context.Background(), "ses_old", provider.RunRequest{RunID: "run-1", Prompt: "continue", WorkDir: dir, Model: "sonnet-test"})
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
	if !strings.Contains(session, "-p --resume ses_old --output-format stream-json --verbose --permission-mode plan --model sonnet-test") {
		t.Fatalf("args=%q", session)
	}
}

func TestAdapterCapturesStopFailureAndExposesQuota(t *testing.T) {
	dir := t.TempDir()
	stopFailure := `{"session_id":"s1","hook_event_name":"StopFailure","error":"rate_limit","error_details":"retry in 2 hours","last_assistant_message":"API Error: Rate limit reached"}`
	result := `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s1","errors":["rate limit"]}`
	posix := strings.Join([]string{
		"settings=\"\"",
		"previous=\"\"",
		"for arg in \"$@\"; do",
		"  if [ \"$previous\" = \"--settings\" ]; then settings=\"$arg\"; fi",
		"  previous=\"$arg\"",
		"done",
		"capture=\"$(dirname \"$settings\")/stop-failure.jsonl\"",
		"printf '%s\\n' '" + stopFailure + "' > \"$capture\"",
		"printf '%s\\n' '" + result + "'",
	}, "\n")
	windows := strings.Join([]string{
		"setlocal enabledelayedexpansion",
		"set prev=",
		"set settings=",
		"for %%a in (%*) do (",
		"  if \"!prev!\"==\"--settings\" set \"settings=%%~a\"",
		"  set \"prev=%%~a\"",
		")",
		"for %%f in (\"!settings!\") do set \"capdir=%%~dpf\"",
		">\"!capdir!stop-failure.jsonl\" echo " + stopFailure,
		"echo " + result,
	}, "\n")
	script := testscript.Write(t, dir, "claude", posix, windows)
	a := New(script)
	events, err := a.Start(context.Background(), provider.RunRequest{RunID: "run-1", Prompt: "work", WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for event := range events {
		if event.Type == provider.EventFailed {
			failed++
		}
	}
	quota, err := a.GetQuota(context.Background(), provider.AccountRef{})
	if err != nil || failed < 1 || !quota.LimitReached || quota.ResetAt == nil || quota.Source != "stop_failure" {
		t.Fatalf("failed=%d quota=%+v err=%v", failed, quota, err)
	}
}

func TestClaudeTelemetryIsExplicitlyOptIn(t *testing.T) {
	t.Setenv("GOALFORGE_CLAUDE_OTEL_ENDPOINT", "")
	if env := claudeTelemetryEnvironment(provider.RunRequest{RunID: "R1"}); env != nil {
		t.Fatalf("unexpected telemetry env=%v", env)
	}
	t.Setenv("GOALFORGE_CLAUDE_OTEL_ENDPOINT", "http://collector:4318")
	env := strings.Join(claudeTelemetryEnvironment(provider.RunRequest{RunID: "R1"}), "\n")
	for _, expected := range []string{"CLAUDE_CODE_ENABLE_TELEMETRY=1", "OTEL_LOGS_EXPORTER=otlp", "goalforge.run.id=R1", "OTEL_METRICS_INCLUDE_ACCOUNT_UUID=false"} {
		if !strings.Contains(env, expected) {
			t.Fatalf("missing %q in %s", expected, env)
		}
	}
}
