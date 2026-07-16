package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/testscript"
)

// runCLI executes one goalforge invocation through the real dispatch and
// returns its stdout, failing the test on error.
func runCLI(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	output, err := runCLIWithError(t, ctx, args...)
	if err != nil {
		t.Fatalf("goalforge %v: %v\noutput:\n%s", args, err, output)
	}
	return output
}

func runCLIWithError(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	previous := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	runErr := run(ctx, args)
	_ = write.Close()
	os.Stdout = previous
	output, _ := io.ReadAll(read)
	return string(output), runErr
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// TestCLIFullLifecycle drives the complete user journey through the real CLI
// dispatch with a contract-faithful fake provider: register, plan, execute in
// an isolated worktree, verify, auto-commit, approve and merge, approve and
// publish, garbage-collect, and hand the goal to the autonomous worker. It
// exists because the CLI layer is where wiring bugs hid (unreachable
// `approval approve`, non-revivable CONTINUE jobs).
func TestCLIFullLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-b", "main")
	gitIn(t, repo, "config", "user.email", "e2e@example.invalid")
	gitIn(t, repo, "config", "user.name", "E2E")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "README.md")
	gitIn(t, repo, "commit", "-m", "base")

	fake := testscript.Write(t, t.TempDir(), "claude",
		strings.Join([]string{
			"cat >/dev/null",
			"printf 'hello goalforge\\n' > hello.txt",
			`printf '{"type":"system","subtype":"init","session_id":"sess-cli-e2e"}\n'`,
			`printf '{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess-cli-e2e","total_cost_usd":0.001,"usage":{"input_tokens":100,"output_tokens":50}}\n'`,
		}, "\n"),
		strings.Join([]string{
			"echo hello goalforge> hello.txt",
			`echo {"type":"system","subtype":"init","session_id":"sess-cli-e2e"}`,
			`echo {"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess-cli-e2e","total_cost_usd":0.001,"usage":{"input_tokens":100,"output_tokens":50}}`,
			"more > nul",
		}, "\n"))
	gate := testscript.Write(t, repo, "verify-gate", "grep goalforge hello.txt", "findstr goalforge hello.txt")
	gitIn(t, repo, "add", filepath.Base(gate))
	gitIn(t, repo, "commit", "-m", "gate")
	remote := t.TempDir()
	gitIn(t, remote, "init", "--bare", "-b", "main")
	gitIn(t, repo, "remote", "add", "origin", remote)

	t.Setenv("GOALFORGE_CLAUDE_BIN", fake)
	t.Setenv("GOALFORGE_DB", filepath.Join(t.TempDir(), "state.db"))
	t.Chdir(repo)

	runCLI(t, ctx, "project", "init", "--name", "e2e", "--provider", "claude", "--model", "haiku", "--worktrees", "--auto-commit")
	runCLI(t, ctx, "goal", "set", "--title", "greeting", "--objective", "write hello.txt", "--criterion", "build_passed=true")
	workOut := runCLI(t, ctx, "work", "add", "--title", "create hello.txt", "--priority", "10", "--scope", "hello.txt")
	workID := regexp.MustCompile(`WORK-\d+`).FindString(workOut)
	if workID == "" {
		t.Fatalf("no work item ID in %q", workOut)
	}
	runCLI(t, ctx, "verify", "gate", "add", "--type", "build_passed", "--command-json", `["`+strings.ReplaceAll(gate, `\`, `\\`)+`"]`, "--timeout-seconds", "30")

	continueOut := runCLI(t, ctx, "continue")
	if !strings.Contains(continueOut, "passed=true") || !strings.Contains(continueOut, "goal_completed=true") {
		t.Fatalf("continue output: %s", continueOut)
	}
	if _, err := os.Stat(filepath.Join(repo, "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("provider change leaked into the base repository: %v", err)
	}
	worktree := repo + ".goalforge-worktrees" + string(filepath.Separator) + workID
	message := gitIn(t, worktree, "log", "-1", "--format=%an %B")
	for _, expected := range []string{"GoalForge", "Work-Item-ID: " + workID, "Run-ID:"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("missing %q in auto-commit: %q", expected, message)
		}
	}
	statusOut := runCLI(t, ctx, "status")
	if !strings.Contains(statusOut, "State: COMPLETED") || !strings.Contains(statusOut, "input=100") {
		t.Fatalf("status output: %s", statusOut)
	}

	// Merge and publish are approval-gated: denied first, then approved.
	if output, err := runCLIWithError(t, ctx, "merge", "--work-item", workID); err == nil {
		t.Fatalf("merge must require approval, got: %s", output)
	}
	approvalOut := runCLI(t, ctx, "approval", "request", "--action", "merge-branch", "--reason", "test")
	approvalID := regexp.MustCompile(`APR-\d+`).FindString(approvalOut)
	runCLI(t, ctx, "approval", "approve", approvalID)
	runCLI(t, ctx, "merge", "--work-item", workID)
	if content, err := os.ReadFile(filepath.Join(repo, "hello.txt")); err != nil || !strings.Contains(string(content), "goalforge") {
		t.Fatalf("merge did not land hello.txt on main: %q err=%v", content, err)
	}

	if output, err := runCLIWithError(t, ctx, "publish", "--work-item", workID); err == nil {
		t.Fatalf("publish must require approval, got: %s", output)
	}
	approvalOut = runCLI(t, ctx, "approval", "request", "--action", "publish-branch", "--reason", "test")
	approvalID = regexp.MustCompile(`APR-\d+`).FindString(approvalOut)
	runCLI(t, ctx, "approval", "approve", approvalID)
	publishOut := runCLI(t, ctx, "publish", "--work-item", workID)
	branch := regexp.MustCompile(`goalforge/\S+`).FindString(publishOut)
	if branch == "" || gitIn(t, remote, "rev-parse", branch) == "" {
		t.Fatalf("published branch missing on remote: %s", publishOut)
	}

	gcOut := runCLI(t, ctx, "worktree", "gc")
	if !strings.Contains(gcOut, "removed 1 of 1") {
		t.Fatalf("gc output: %s", gcOut)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree survived gc: %v", err)
	}

	// Autonomous path: a revived CONTINUE job on a completed project exits
	// cleanly through the worker.
	runCLI(t, ctx, "continue", "--enqueue")
	workerOut := runCLI(t, ctx, "worker", "--once")
	if !strings.Contains(workerOut, "job_processed=true") {
		t.Fatalf("worker output: %s", workerOut)
	}
	workListOut := runCLI(t, ctx, "work", "list")
	if !strings.Contains(workListOut, "no active goal") {
		t.Fatalf("work list output: %s", workListOut)
	}
}
