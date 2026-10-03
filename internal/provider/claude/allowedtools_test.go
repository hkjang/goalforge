package claude

import (
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
)

// acceptEdits approves edits and still asks before running a shell command, and
// a headless session has nobody to ask. So a session that wanted to run the
// project's tests before saying it was finished could not.
//
// Claude's own help documents the form as "Bash(git *)", so a prefix is the
// glob and the bare command needs its own entry: Bash(go test *) does not match
// `go test` with no arguments.
func TestAllowedCommandsBecomeBashRules(t *testing.T) {
	args := strings.Join(permissionArgs(provider.RunRequest{WorkspaceWrite: true,
		AllowedCommands: []string{"go test", "go test -count=1 ./..."}}), " ")
	if !strings.Contains(args, "--allowedTools") {
		t.Fatalf("args=%s", args)
	}
	for _, want := range []string{
		"Bash(go test)", "Bash(go test *)",
		"Bash(go test -count=1 ./...)", "Bash(go test -count=1 ./... *)",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
}

// Nothing allowed means no flag. An empty allowlist is a different thing from an
// absent one.
func TestNoAllowedCommandsPassesNoFlag(t *testing.T) {
	args := strings.Join(permissionArgs(provider.RunRequest{WorkspaceWrite: true}), " ")
	if strings.Contains(args, "--allowedTools") {
		t.Fatalf("args=%s", args)
	}
	if !strings.Contains(args, "acceptEdits") {
		t.Fatalf("args=%s", args)
	}
}

// A planning run allows nothing: it looks rather than acts, and pre-approved
// shell commands would make the mode a label.
func TestAPlanningRunAllowsNoCommands(t *testing.T) {
	args := strings.Join(permissionArgs(provider.RunRequest{
		AllowedCommands: []string{"go test"}}), " ")
	if strings.Contains(args, "--allowedTools") {
		t.Fatalf("args=%s", args)
	}
	if !strings.Contains(args, "plan") {
		t.Fatalf("args=%s", args)
	}
}

// Start and Resume get the same permissions. A session that could run the tests
// on its first turn and not its second would stop halfway through its own
// checking — and the two blocks were written out separately, which is how they
// drift apart.
func TestStartAndResumeAgreeOnPermissions(t *testing.T) {
	request := provider.RunRequest{WorkspaceWrite: true, AllowedCommands: []string{"go test"}}
	start := strings.Join(startArgs(request), " ")
	resume := strings.Join(resumeArgs("S1", request), " ")
	for _, want := range []string{"acceptEdits", "Bash(go test)"} {
		if !strings.Contains(start, want) {
			t.Fatalf("start missing %q: %s", want, start)
		}
		if !strings.Contains(resume, want) {
			t.Fatalf("resume missing %q: %s", want, resume)
		}
	}
}

// A command carrying a comma cannot be expressed: the flag's value is comma or
// space separated, so one would be torn into two rules that each match nothing —
// an allowlist that looks configured and admits nothing.
func TestACommandThatCannotBeExpressedIsDropped(t *testing.T) {
	args := strings.Join(permissionArgs(provider.RunRequest{WorkspaceWrite: true,
		AllowedCommands: []string{"go test", "go test -run A,B", "weird (paren)"}}), " ")
	if !strings.Contains(args, "Bash(go test)") {
		t.Fatalf("the usable one survives: %s", args)
	}
	for _, unwanted := range []string{"A,B", "(paren)"} {
		if strings.Contains(args, unwanted) {
			t.Fatalf("%q cannot be expressed: %s", unwanted, args)
		}
	}
}
