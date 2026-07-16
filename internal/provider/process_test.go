package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessRunnerStreamsAndUsesStdin(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-provider")
	content := "#!/bin/sh\nread prompt\nprintf '{\"type\":\"message\",\"prompt\":\"%s\"}\\n' \"$prompt\"\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
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
