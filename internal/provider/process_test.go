package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/testscript"
)

func TestProcessRunnerStreamsAndUsesStdin(t *testing.T) {
	dir := t.TempDir()
	script := testscript.Write(t, dir, "fake-provider",
		"read prompt\nprintf '{\"type\":\"message\",\"prompt\":\"%s\"}\\n' \"$prompt\"",
		"set /p prompt=\necho {\"type\":\"message\",\"prompt\":\"%prompt%\"}")
	runner := NewProcessRunner(script)
	events, err := runner.Run(context.Background(), RunRequest{RunID: "run-1", Prompt: "safe prompt\n", WorkDir: dir}, []string{"--json"}, func(line []byte) ([]Event, error) { return []Event{{Type: EventMessage, Message: string(line)}}, nil })
	if err != nil {
		t.Fatal(err)
	}
	var got []Event
	for event := range events {
		got = append(got, event)
	}
	if len(got) != 1 || !strings.Contains(got[0].Message, "safe prompt") {
		t.Fatalf("events=%+v", got)
	}
}
