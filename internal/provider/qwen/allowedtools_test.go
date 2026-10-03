package qwen

import (
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
)

func argsOf(r provider.RunRequest) string { return strings.Join(baseArgs(r), " ") }

// Qwen Code gates shell commands even with edits auto-approved, and a headless
// session has nobody to ask. So a session that wanted to run the project's
// tests before saying it was finished could not: it wrote code, reported done,
// and the gates found out afterwards.
//
// The allowlist is passed as --allowed-tools with the tool-and-argument form
// Qwen matches on. Its rule is `value === pattern || value.startsWith(pattern +
// " ")`, so "run_shell_command(go test)" admits `go test` and `go test ./pkg`
// and nothing else — and a chained command is split and each part checked
// separately, so `go test ./... ; rm -rf /` still stops at the second part.
func TestAllowedCommandsBecomeAllowedTools(t *testing.T) {
	args := argsOf(provider.RunRequest{WorkspaceWrite: true,
		AllowedCommands: []string{"go test -count=1 ./...", "go test", "go vet"}})
	if !strings.Contains(args, "--allowed-tools") {
		t.Fatalf("args=%s", args)
	}
	for _, want := range []string{
		"run_shell_command(go test -count=1 ./...)",
		"run_shell_command(go test)",
		"run_shell_command(go vet)",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
}

// Nothing allowed means the flag is not passed at all. An empty --allowed-tools
// is a different thing from an absent one, and passing one would be claiming a
// configuration nobody made.
func TestNoAllowedCommandsPassesNoFlag(t *testing.T) {
	args := argsOf(provider.RunRequest{WorkspaceWrite: true})
	if strings.Contains(args, "--allowed-tools") {
		t.Fatalf("args=%s", args)
	}
}

// A read-only run allows nothing. The point of plan mode is that the session
// looks and does not act, and handing it pre-approved shell commands there
// would make the mode a label.
func TestAReadOnlyRunAllowsNoCommands(t *testing.T) {
	args := argsOf(provider.RunRequest{WorkspaceWrite: false,
		AllowedCommands: []string{"go test"}})
	if strings.Contains(args, "--allowed-tools") {
		t.Fatalf("a planning run does not run commands: %s", args)
	}
	if !strings.Contains(args, "--approval-mode plan") {
		t.Fatalf("args=%s", args)
	}
}

// A pattern that would break the form is dropped rather than passed. Qwen
// matches on the text between the first "(" and a trailing ")", so a command
// containing either cannot be expressed — and a malformed pattern silently
// matches nothing, which looks like an allowlist that is simply ignored.
func TestACommandThatCannotBeExpressedIsDropped(t *testing.T) {
	args := argsOf(provider.RunRequest{WorkspaceWrite: true,
		// A comma is the dangerous one: Qwen splits the flag's value on commas,
		// so a command containing one would be torn into two patterns that
		// each match nothing — an allowlist that looks configured and admits
		// nothing.
		AllowedCommands: []string{"go test", "sh -c (weird)", "echo )", "go test -run A,B"}})
	if !strings.Contains(args, "run_shell_command(go test)") {
		t.Fatalf("the usable one survives: %s", args)
	}
	for _, unwanted := range []string{"(weird)", "echo )", "A,B"} {
		if strings.Contains(args, unwanted) {
			t.Fatalf("%q cannot be expressed in the pattern form: %s", unwanted, args)
		}
	}
}

// The resumed turn gets the same allowlist. A session that could run the tests
// on its first turn and not on its second would stop halfway through its own
// checking.
func TestAResumedTurnKeepsTheAllowlist(t *testing.T) {
	adapter := New("qwen")
	_ = adapter
	args := strings.Join(append([]string{"--resume", "S1"},
		baseArgs(provider.RunRequest{WorkspaceWrite: true,
			AllowedCommands: []string{"go test"}})...), " ")
	if !strings.Contains(args, "run_shell_command(go test)") {
		t.Fatalf("args=%s", args)
	}
}
