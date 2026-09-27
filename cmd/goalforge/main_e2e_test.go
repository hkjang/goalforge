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
	if output, err := runCLIWithError(t, ctx, "approval", "request", "--action", "merge-branch", "--reason", "test"); err == nil {
		t.Fatalf("merge approval must name the work item being approved, got: %s", output)
	}
	approvalOut := runCLI(t, ctx, "approval", "request", "--action", "merge-branch", "--work-item", workID, "--reason", "test")
	approvalID := regexp.MustCompile(`APR-\d+`).FindString(approvalOut)
	runCLI(t, ctx, "approval", "approve", approvalID)
	runCLI(t, ctx, "merge", "--work-item", workID)
	if content, err := os.ReadFile(filepath.Join(repo, "hello.txt")); err != nil || !strings.Contains(string(content), "goalforge") {
		t.Fatalf("merge did not land hello.txt on main: %q err=%v", content, err)
	}

	if output, err := runCLIWithError(t, ctx, "publish", "--work-item", workID); err == nil {
		t.Fatalf("publish must require approval, got: %s", output)
	}
	approvalOut = runCLI(t, ctx, "approval", "request", "--action", "publish-branch", "--work-item", workID, "--reason", "test")
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

// TestCLIEvidenceAndPreviewLifecycle drives the commands added after the
// original lifecycle test through the real dispatch: readiness diagnosis, the
// dry-run preview, design decisions, human takeover and return, integration
// verification, the evidence bundle, failure reproduction, and the evaluation
// registry. The original test froze at the feature set of its day, so every
// command added since had only unit coverage — which is exactly where the
// wiring bugs in this codebase have hidden (flags lost to positional parsing,
// helpers deleted by an edit elsewhere).
func TestCLIEvidenceAndPreviewLifecycle(t *testing.T) {
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

	// A real provider CLI does no work for --version or --help; the fake has
	// to behave the same, or `doctor` probing it would write into the
	// repository it is diagnosing.
	fake := testscript.Write(t, t.TempDir(), "claude",
		strings.Join([]string{
			`case "$*" in *--version*|*--help*) echo "fake 1.0 --output-format --resume --settings --permission-mode --json-schema --no-session-persistence"; exit 0;; esac`,
			"cat >/dev/null",
			"printf 'hello goalforge\\n' > hello.txt",
			`printf '{"type":"system","subtype":"init","session_id":"sess-evidence"}\n'`,
			`printf '{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess-evidence","total_cost_usd":0.002,"usage":{"input_tokens":200,"output_tokens":80}}\n'`,
		}, "\n"),
		strings.Join([]string{
			`echo %* | findstr /C:"--version" >nul && (echo fake 1.0 && exit /b 0)`,
			`echo %* | findstr /C:"--help" >nul && (echo --output-format --resume --settings --permission-mode --json-schema --no-session-persistence && exit /b 0)`,
			"echo hello goalforge> hello.txt",
			`echo {"type":"system","subtype":"init","session_id":"sess-evidence"}`,
			`echo {"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess-evidence","total_cost_usd":0.002,"usage":{"input_tokens":200,"output_tokens":80}}`,
			"more > nul",
		}, "\n"))
	gate := testscript.Write(t, repo, "verify-gate", "grep goalforge hello.txt", "findstr goalforge hello.txt")
	gitIn(t, repo, "add", filepath.Base(gate))
	gitIn(t, repo, "commit", "-m", "gate")

	t.Setenv("GOALFORGE_CLAUDE_BIN", fake)
	t.Setenv("GOALFORGE_DB", filepath.Join(t.TempDir(), "state.db"))
	t.Chdir(repo)

	runCLI(t, ctx, "project", "init", "--name", "evidence", "--provider", "claude", "--model", "haiku", "--worktrees", "--auto-commit")

	// Readiness must refuse a project that could never complete, before any
	// model call is spent on it. The fake provider cannot satisfy the adapter
	// flag check, so the readiness findings are asserted directly rather than
	// through the overall verdict.
	runCLI(t, ctx, "goal", "set", "--title", "greeting", "--objective", "write hello.txt",
		"--criterion", "build_passed=true", "--criterion", "latency_p95=200")
	noGates, err := runCLIWithError(t, ctx, "doctor")
	if err == nil || !strings.Contains(noGates, "검증 게이트가 없어") {
		t.Fatalf("doctor must block a project with no gates:\n%s", noGates)
	}
	runCLI(t, ctx, "verify", "gate", "add", "--type", "build_passed",
		"--command-json", `["`+strings.ReplaceAll(gate, `\`, `\\`)+`"]`, "--timeout-seconds", "30")
	unmeasurable, err := runCLIWithError(t, ctx, "doctor")
	if err == nil || !strings.Contains(unmeasurable, "criteria coverage") || !strings.Contains(unmeasurable, "latency_p95") {
		t.Fatalf("doctor must name the criterion nothing measures:\n%s", unmeasurable)
	}
	// Dropping the unmeasurable criterion is a goal change, and the readiness
	// finding has to clear once every criterion has a gate.
	runCLI(t, ctx, "goal", "set", "--title", "greeting", "--objective", "write hello.txt",
		"--criterion", "build_passed=true", "--reason", "측정할 수 없는 조건 제거")
	covered, _ := runCLIWithError(t, ctx, "doctor")
	if !strings.Contains(covered, "OK   criteria coverage") {
		t.Fatalf("criteria coverage should clear:\n%s", covered)
	}

	// The preview refuses before there is work, and explains itself once
	// there is.
	if output, err := runCLIWithError(t, ctx, "plan"); err == nil {
		t.Fatalf("plan must refuse with no work item:\n%s", output)
	}
	workOut := runCLI(t, ctx, "work", "add", "--title", "create hello.txt", "--priority", "10", "--scope", "hello.txt")
	workID := regexp.MustCompile(`WORK-\d+`).FindString(workOut)
	planOut := runCLI(t, ctx, "plan")
	for _, expected := range []string{workID, "다음 작업", "forecast", "model"} {
		if !strings.Contains(planOut, expected) {
			t.Fatalf("plan output missing %q:\n%s", expected, planOut)
		}
	}
	// Previewing must not consume the work it previews.
	if !strings.Contains(runCLI(t, ctx, "work", "list"), "BACKLOG") {
		t.Fatalf("the preview claimed the work item:\n%s", runCLI(t, ctx, "work", "list"))
	}

	runCLI(t, ctx, "decision", "add", "--title", "단일 파일 출력",
		"--decision", "결과는 hello.txt 하나로 쓴다", "--alternatives", "여러 파일: 검증이 복잡해짐")
	if !strings.Contains(runCLI(t, ctx, "decision", "list"), "단일 파일 출력") {
		t.Fatal("recorded decision is not listed")
	}

	continueOut := runCLI(t, ctx, "continue")
	if !strings.Contains(continueOut, "passed=true") {
		t.Fatalf("continue output:\n%s", continueOut)
	}
	runID := regexp.MustCompile(`RUN-\d+`).FindString(continueOut)

	// Status reports the criterion with the evidence that decided it.
	statusOut := runCLI(t, ctx, "status")
	if !strings.Contains(statusOut, "[v]") || !strings.Contains(statusOut, "build_passed") {
		t.Fatalf("status must show the criterion verdict:\n%s", statusOut)
	}

	// A reproduction package is assembled from a real run.
	reproDir := filepath.Join(t.TempDir(), "repro")
	runCLI(t, ctx, "reproduce", "--run", runID, "--out", reproDir)
	for _, name := range []string{"reproduction.json", "reproduce.sh", "gate-output.log"} {
		if _, err := os.Stat(filepath.Join(reproDir, name)); err != nil {
			t.Fatalf("reproduction package missing %s: %v", name, err)
		}
	}

	// Merging leaves the default branch unverified until integration runs,
	// and that must be visible in status rather than only in the database.
	runCLI(t, ctx, "approval", "request", "--action", "merge-branch", "--work-item", workID, "--reason", "e2e")
	approvalID := regexp.MustCompile(`APR-\d+`).FindString(runCLI(t, ctx, "approval", "list"))
	if approvalID == "" {
		t.Fatal("no pending approval to approve")
	}
	runCLI(t, ctx, "approval", "approve", approvalID)
	mergeOut := runCLI(t, ctx, "merge", "--work-item", workID)
	if !strings.Contains(mergeOut, "integration verification required") {
		t.Fatalf("merge must ask for integration verification:\n%s", mergeOut)
	}
	if !strings.Contains(runCLI(t, ctx, "status"), "통합 검증 필요") {
		t.Fatalf("status must surface the integration gap:\n%s", runCLI(t, ctx, "status"))
	}
	integrationOut := runCLI(t, ctx, "verify", "integration")
	if !strings.Contains(integrationOut, "integration verified") {
		t.Fatalf("integration verification:\n%s", integrationOut)
	}
	if verified := runCLI(t, ctx, "status"); !strings.Contains(verified, "[v]") {
		t.Fatalf("integration evidence should satisfy the criterion:\n%s", verified)
	}

	// AT-10: changing the code the integration check measured must invalidate
	// its evidence. Relying on each mutation site to declare that is how
	// evidence outlives the thing it describes.
	if err := os.WriteFile(filepath.Join(repo, "unverified.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := runCLI(t, ctx, "status")
	if !strings.Contains(stale, "[~]") || !strings.Contains(stale, "재검증 필요") {
		t.Fatalf("evidence must go stale once the measured tree changes:\n%s", stale)
	}
	// Re-running the check against the current tree restores it.
	runCLI(t, ctx, "verify", "integration")
	if refreshed := runCLI(t, ctx, "status"); !strings.Contains(refreshed, "[v]") {
		t.Fatalf("re-verification should restore the evidence:\n%s", refreshed)
	}

	// The evidence bundle is written and carries the decision and the gate.
	evidenceDir := filepath.Join(t.TempDir(), "evidence")
	runCLI(t, ctx, "evidence", "export", "--out", evidenceDir)
	page, err := os.ReadFile(filepath.Join(evidenceDir, "evidence.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"greeting", "build_passed", "단일 파일 출력", "MERGE_BRANCH"} {
		if !strings.Contains(string(page), expected) {
			t.Fatalf("evidence bundle missing %q", expected)
		}
	}
	if _, err = os.Stat(filepath.Join(evidenceDir, "evidence.json")); err != nil {
		t.Fatal(err)
	}

	// Evaluation registry and reporting round-trip through the CLI.
	evalOut := runCLI(t, ctx, "eval", "add", "--name", "hello", "--kind", "feature", "--objective", "write hello.txt")
	evalID := regexp.MustCompile(`EVAL-\d+`).FindString(evalOut)
	runCLI(t, ctx, "eval", "record", "--case", evalID, "--label", "haiku", "--run", runID)
	if !strings.Contains(runCLI(t, ctx, "eval", "compare"), "haiku") {
		t.Fatal("evaluation comparison lost the label")
	}
	if !strings.Contains(runCLI(t, ctx, "report", "--since", "1h"), "runs=") {
		t.Fatal("report produced no run summary")
	}
	if !strings.Contains(runCLI(t, ctx, "models"), "selected:") {
		t.Fatal("model advice produced no selection")
	}
}
