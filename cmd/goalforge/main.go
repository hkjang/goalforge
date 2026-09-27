package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/goalforge/goalforge/internal/api"
	"github.com/goalforge/goalforge/internal/app"
	"github.com/goalforge/goalforge/internal/diagnostics"
	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/mcp"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/notify"
	"github.com/goalforge/goalforge/internal/orchestrator"
	"github.com/goalforge/goalforge/internal/planner"
	"github.com/goalforge/goalforge/internal/policy"
	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/provider/claude"
	"github.com/goalforge/goalforge/internal/provider/codex"
	"github.com/goalforge/goalforge/internal/provider/opencode"
	"github.com/goalforge/goalforge/internal/provider/qwen"
	"github.com/goalforge/goalforge/internal/scheduler"
	pgstore "github.com/goalforge/goalforge/internal/store/postgres"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	usagepolicy "github.com/goalforge/goalforge/internal/usage"
	"github.com/goalforge/goalforge/internal/verification"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return usage()
	}
	dbPath := os.Getenv("GOALFORGE_DB")
	if dbPath == "" {
		dbPath = filepath.Join(".goalforge", "goalforge.db")
	}
	if args[0] == "--db" {
		if len(args) < 3 {
			return errors.New("--db requires a path and command")
		}
		dbPath, args = args[1], args[2:]
	}
	if len(args) > 2 && args[0] == "storage" && args[1] == "postgres" && args[2] == "migrate" {
		return postgresMigrate(ctx, args[3:])
	}
	s, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	switch args[0] {
	case "project":
		if len(args) > 1 && args[1] == "init" {
			return projectInit(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "budget" {
			return projectBudget(ctx, s, args[2:])
		}
		if len(args) > 2 && args[1] == "provider" && args[2] == "set" {
			return projectProviderSet(ctx, s, args[3:])
		}
		if len(args) > 1 && args[1] == "runtime" {
			return projectRuntime(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "concurrency" {
			return projectConcurrency(ctx, s, args[2:])
		}
	case "goal":
		if len(args) > 1 && args[1] == "set" {
			return goalSet(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "show" {
			return goalShow(ctx, s)
		}
	case "status":
		return goalShow(ctx, s)
	case "milestone":
		if len(args) > 1 && args[1] == "add" {
			return milestoneAdd(ctx, s, args[2:])
		}
	case "work":
		if len(args) > 1 && args[1] == "add" {
			return workAdd(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "list" {
			return workList(ctx, s)
		}
		if len(args) > 2 && args[1] == "status" {
			return workStatus(ctx, s, args[2:])
		}
	case "verify":
		if len(args) > 1 && args[1] == "record" {
			return verifyRecord(ctx, s, args[2:])
		}
		if len(args) > 2 && args[1] == "gate" && args[2] == "add" {
			return gateAdd(ctx, s, args[3:])
		}
		if len(args) > 1 && args[1] == "integration" {
			return verifyIntegration(ctx, s, args[2:])
		}
	case "continue":
		return continueGoal(ctx, s, args[1:], false)
	case "develop":
		return continueGoal(ctx, s, args[1:], true)
	case "ideas":
		return ideasGoal(ctx, s)
	case "audit":
		return auditGoal(ctx, s)
	case "replan":
		return replanGoal(ctx, s)
	case "usage":
		return usageShow(ctx, s)
	case "sessions":
		return sessionsShow(ctx, s)
	case "checkpoint":
		return checkpointCreate(ctx, s, args[1:])
	case "logs":
		return logsShow(ctx, s, args[1:])
	case "report":
		return activityReport(ctx, s, args[1:])
	case "models":
		return modelAdvice(ctx, s, args[1:])
	case "cancel":
		return cancelScheduled(ctx, s)
	case "pause":
		return pauseExecution(ctx, s)
	case "resume":
		return resumePaused(ctx, s)
	case "worktree":
		if len(args) > 1 && args[1] == "gc" {
			return worktreeGC(ctx, s, args[2:])
		}
	case "publish":
		return publishWork(ctx, s, args[1:])
	case "merge":
		return mergeWork(ctx, s, args[1:])
	case "rollback":
		return rollbackWork(ctx, s, args[1:])
	case "approval":
		if len(args) > 1 && args[1] == "request" {
			return approvalRequest(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "approve" {
			return approvalApprove(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "reject" {
			return approvalReject(ctx, s, args[2:])
		}
	case "doctor":
		return runDoctor(ctx, s, args[1:])
	case "worker":
		return runWorker(ctx, s, args[1:])
	case "serve":
		return serveAPI(ctx, s, args[1:])
	case "mcp":
		return mcpServe(ctx, s, args[1:])
	case "run":
		if len(args) > 1 && args[1] == "--until-quota" {
			return runUntilQuota(ctx, s, args[2:])
		}
	}
	return usage()
}

func postgresMigrate(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("storage postgres migrate", flag.ContinueOnError)
	dsn := f.String("dsn", os.Getenv("GOALFORGE_POSTGRES_DSN"), "PostgreSQL DSN (or GOALFORGE_POSTGRES_DSN)")
	if err := f.Parse(args); err != nil {
		return err
	}
	s, err := pgstore.Open(ctx, *dsn)
	if err != nil {
		return err
	}
	defer s.Close()
	fmt.Println("PostgreSQL GoalForge scheduler schema is current")
	return nil
}

func usage() error {
	return errors.New("usage: goalforge [--db PATH] project init|project budget|project runtime|project provider set|goal set|goal show|milestone add|work add|work list|work status ID|verify gate add|ideas|audit|replan|continue|develop|run --until-quota|status|usage|sessions|checkpoint|logs|pause|resume|rollback|worktree gc|publish|merge|doctor|cancel|approval request|approval approve ID|approval reject ID|worker [--once]|serve|mcp [--addr HOST:PORT]")
}

func serveAPI(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := f.String("addr", "127.0.0.1:8787", "HTTP listen address")
	if err := f.Parse(args); err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return fmt.Errorf("invalid --addr: %w", err)
	}
	token := os.Getenv("GOALFORGE_API_TOKEN")
	ip := net.ParseIP(host)
	loopback := host == "localhost" || ip != nil && ip.IsLoopback()
	if !loopback && token == "" {
		return errors.New("GOALFORGE_API_TOKEN is required for non-loopback listen addresses")
	}
	handler, err := api.New(s, token)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *addr, Handler: handler.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Printf("GoalForge UI: http://%s\n", *addr)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
		return nil
	}
	return err
}

func projectRuntime(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("project runtime", flag.ContinueOnError)
	turnTimeout := f.Duration("turn-timeout", 30*time.Minute, "maximum provider turn duration")
	runTimeout := f.Duration("run-timeout", 2*time.Hour, "maximum orchestrator run duration")
	if err := f.Parse(args); err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	policy := store.RuntimePolicy{TurnTimeout: *turnTimeout, RunTimeout: *runTimeout}
	if err = s.SetRuntimePolicy(ctx, project.ID, policy); err != nil {
		return err
	}
	fmt.Printf("runtime policy set: turn_timeout=%s run_timeout=%s\n", policy.TurnTimeout, policy.RunTimeout)
	return nil
}

// mergeWork merges a verified work branch into the project's default branch.
// Like publish it is manual and approval-gated; conflicts are aborted for
// user review rather than auto-resolved.
func mergeWork(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("merge", flag.ContinueOnError)
	workItemID := f.String("work-item", "", "work item whose verified branch to merge")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *workItemID == "" {
		return errors.New("--work-item is required")
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	commit, err := s.LatestRunCommitForWork(ctx, project.ID, *workItemID)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("work item %s has no verified commit; only verified work can be merged", *workItemID)
	}
	if err != nil {
		return err
	}
	scope := store.ApprovalScope{WorkItemID: *workItemID, SourceBranch: commit.Branch, TargetRef: project.DefaultBranch, CommitSHA: commit.CommitSHA, FilesChanged: commit.FilesCommitted}
	approved, err := s.ConsumeScopedApproval(ctx, project.ID, store.ApprovalMergeBranch, "merge:"+*workItemID, scope)
	if err != nil {
		return err
	}
	if !approved {
		return fmt.Errorf("merging %s into %s requires approval of that commit: run `goalforge approval request --action merge-branch --work-item %s --reason ...` and approve it first",
			shortSHA(commit.CommitSHA), project.DefaultBranch, *workItemID)
	}
	message := "Merge verified work " + *workItemID + "\n\nGoal-ID: " + commit.GoalID + "\nWork-Item-ID: " + commit.WorkItemID + "\nRun-ID: " + commit.RunID + "\n"
	sha, err := gitops.MergeVerified(ctx, project.RepositoryPath, project.DefaultBranch, commit.Branch, message)
	if err != nil {
		return err
	}
	if err = s.MarkIntegrationPending(ctx, project.ID, "병합 후 통합 검증이 필요합니다: "+*workItemID, sha); err != nil {
		return err
	}
	fmt.Printf("merged: branch=%s into=%s commit=%s work=%s\n", commit.Branch, project.DefaultBranch, sha, *workItemID)
	fmt.Println("integration verification required: run `goalforge verify integration` — each item verified in its own worktree, not merged together")
	return nil
}

// publishWork pushes a verified work branch to a remote. It is deliberately
// manual and approval-gated (SEC-011): runs never push on their own, and a
// PUBLISH_BRANCH approval must exist before anything leaves the machine.
func publishWork(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("publish", flag.ContinueOnError)
	workItemID := f.String("work-item", "", "work item whose verified branch to push")
	remote := f.String("remote", "origin", "git remote to push to")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *workItemID == "" {
		return errors.New("--work-item is required")
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	commit, err := s.LatestRunCommitForWork(ctx, project.ID, *workItemID)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("work item %s has no verified commit; only verified work can be published", *workItemID)
	}
	if err != nil {
		return err
	}
	scope := store.ApprovalScope{WorkItemID: *workItemID, SourceBranch: commit.Branch, TargetRef: *remote, CommitSHA: commit.CommitSHA, FilesChanged: commit.FilesCommitted}
	approved, err := s.ConsumeScopedApproval(ctx, project.ID, store.ApprovalPublishBranch, "publish:"+*workItemID, scope)
	if err != nil {
		return err
	}
	if !approved {
		return fmt.Errorf("publishing %s to %s requires approval of that commit: run `goalforge approval request --action publish-branch --work-item %s --remote %s --reason ...` and approve it first",
			shortSHA(commit.CommitSHA), *remote, *workItemID, *remote)
	}
	if err = gitops.PushBranch(ctx, project.RepositoryPath, *remote, commit.Branch); err != nil {
		return err
	}
	fmt.Printf("published: branch=%s commit=%s remote=%s work=%s\n", commit.Branch, commit.CommitSHA, *remote, *workItemID)
	return nil
}

func worktreeGC(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("worktree gc", flag.ContinueOnError)
	force := f.Bool("force", false, "also remove worktrees with uncommitted changes")
	if err := f.Parse(args); err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	candidates, err := s.WorktreesForCleanup(ctx, project.ID)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		fmt.Println("no completed or discarded worktrees to clean up")
		return nil
	}
	removed := 0
	for _, record := range candidates {
		worktree := gitops.Worktree{Path: record.Path, Branch: record.Branch, BaseCommit: record.BaseCommit}
		if removeErr := gitops.RemoveWorktree(ctx, project.RepositoryPath, worktree, *force); removeErr != nil {
			if errors.Is(removeErr, gitops.ErrWorktreeDirty) {
				fmt.Printf("skipped\t%s\t%s (uncommitted changes; rerun with --force to discard)\n", record.WorkItemID, record.Path)
				continue
			}
			return removeErr
		}
		if markErr := s.MarkWorktreeRemoved(ctx, project.ID, record.WorkItemID); markErr != nil {
			return markErr
		}
		fmt.Printf("removed\t%s\t%s (branch %s kept)\n", record.WorkItemID, record.Path, record.Branch)
		removed++
	}
	fmt.Printf("worktree gc: removed %d of %d candidates\n", removed, len(candidates))
	return nil
}

func rollbackWork(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("rollback", flag.ContinueOnError)
	workItemID := f.String("work-item", "", "work item ID")
	reason := f.String("reason", "user requested rollback to the worktree base checkpoint", "rollback reason")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *workItemID == "" {
		return errors.New("--work-item is required")
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	record, err := s.WorktreeForWorkItem(ctx, project.ID, *workItemID)
	if err != nil {
		return fmt.Errorf("load worktree: %w", err)
	}
	changes, err := s.LatestRunFileChangesForWork(ctx, project.ID, *workItemID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	worktree := gitops.Worktree{Path: record.Path, Branch: record.Branch, BaseCommit: record.BaseCommit}
	if err = gitops.RollbackWorktree(ctx, worktree, changes); err != nil {
		return err
	}
	if err = s.RecordRollback(ctx, project.ID, *workItemID, record, *reason); err != nil {
		return err
	}
	fmt.Printf("rolled back: work=%s branch=%s target=%s\n", *workItemID, record.Branch, record.BaseCommit)
	return nil
}

// projectConcurrency sets how many work items may be implemented at once.
func projectConcurrency(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("project concurrency", flag.ContinueOnError)
	wip := f.Int("wip", 1, "work items that may be implemented at once (disjoint change scopes only)")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if err = s.SetWIPLimit(ctx, p.ID, *wip); err != nil {
		return err
	}
	fmt.Printf("concurrency set: wip_limit=%d\n", *wip)
	if *wip > 1 {
		fmt.Println("note: items only run together when their declared change scopes are disjoint, and a merged result still needs its own verification")
	}
	return nil
}

func projectProviderSet(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("project provider set", flag.ContinueOnError)
	providerName := f.String("provider", "", "target provider: codex, claude, qwen, or opencode")
	modelName := f.String("model", "", "target model")
	reason := f.String("reason", "provider transition requested by user", "handoff reason")
	if err := f.Parse(args); err != nil {
		return err
	}
	if !isSupportedProvider(*providerName) {
		return fmt.Errorf("--provider must be one of %s", strings.Join(supportedProviders, ", "))
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	handoff, err := s.SwitchProvider(ctx, project.ID, *providerName, *modelName, *reason)
	if err != nil {
		return err
	}
	fmt.Printf("provider switched: %s -> %s model=%s handoff=%s\n", handoff.FromProvider, handoff.ToProvider, handoff.ToModel, handoff.ID)
	return nil
}

func runUntilQuota(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("run --until-quota", flag.ContinueOnError)
	maxRuns := f.Int("max-runs", 100, "maximum consecutive work-item runs")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *maxRuns <= 0 {
		return errors.New("--max-runs must be positive")
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	service, cleanup, err := runtimeService(ctx, s, project)
	if err != nil {
		return err
	}
	defer cleanup()
	failures := 0
	for index := 0; index < *maxRuns; index++ {
		project, err = s.ProjectByID(ctx, project.ID)
		if err != nil {
			return err
		}
		if project.State == "COMPLETED" {
			fmt.Printf("goal completed after %d runs\n", index)
			return nil
		}
		result, runErr := service.Continue(ctx, project)
		if errors.Is(runErr, orchestrator.ErrWaitingQuota) {
			fmt.Printf("quota wait scheduled: state=%s completed_runs=%d\n", result.Run.State, index)
			return nil
		}
		if runErr != nil {
			failures++
			kind := policy.ClassifyFailure(runErr, "")
			decision := policy.DecideRetry(kind, failures, 3, policy.ParseRetryAfter(runErr.Error()), rand.Float64)
			switch decision.Action {
			case policy.WaitQuotaReset:
				fmt.Printf("quota exhausted (%s); stopping until reset\n", kind)
				return runErr
			case policy.UseFallbackModel:
				if project.FallbackModel == "" || project.FallbackModel == project.Model {
					return fmt.Errorf("%s (no approved fallback model configured): %w", decision.Reason, runErr)
				}
				if recoverErr := recoverIfFailed(ctx, s, project.ID); recoverErr != nil {
					return errors.Join(runErr, recoverErr)
				}
				if _, switchErr := s.SwitchProvider(ctx, project.ID, project.Provider, project.FallbackModel, "approved fallback model after unsupported model failure"); switchErr != nil {
					return errors.Join(runErr, switchErr)
				}
				fmt.Printf("switched to approved fallback model %s\n", project.FallbackModel)
				continue
			case policy.NewSessionFromCkpt:
				fmt.Printf("session unusable (%s); retrying with a fresh session\n", kind)
				if recoverErr := recoverIfFailed(ctx, s, project.ID); recoverErr != nil {
					return errors.Join(runErr, recoverErr)
				}
				continue
			case policy.RetryAfterDelay:
				fmt.Printf("retryable failure (%s): %v; retrying in %s\n", kind, runErr, decision.Delay.Round(time.Second))
				if waitErr := policy.WaitForRetry(ctx, decision); waitErr != nil {
					return errors.Join(runErr, waitErr)
				}
				if recoverErr := recoverIfFailed(ctx, s, project.ID); recoverErr != nil {
					return errors.Join(runErr, recoverErr)
				}
				continue
			default:
				return fmt.Errorf("%s: %w", decision.Reason, runErr)
			}
		}
		failures = 0
		fmt.Printf("run %d: work=%s state=%s verified=%t progress=%.1f%%\n", index+1, result.WorkItem.ID, result.Run.State, result.Verification.Passed, result.Verification.Progress)
		if result.Verification.GoalCompleted {
			return nil
		}
	}
	return fmt.Errorf("maximum consecutive run limit reached: %d", *maxRuns)
}

func providerBinary(providerName string) string { return diagnostics.Binary(providerName) }

func isSupportedProvider(name string) bool { return diagnostics.IsSupported(name) }

var supportedProviders = diagnostics.Supported

// runDoctor diagnoses the failure modes that otherwise only surface mid-run:
// missing tools, unauthenticated or incompatible provider CLIs, and an
// unregistered project.
func runDoctor(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("doctor", flag.ContinueOnError)
	probeAuth := f.Bool("probe-auth", false, "send one minimal provider request to verify authentication (consumes a small amount of quota)")
	if err := f.Parse(args); err != nil {
		return err
	}
	options := diagnostics.Options{ProbeAuth: *probeAuth}
	cwd, _ := os.Getwd()
	project, projectErr := s.ProjectByPath(ctx, cwd)
	switch {
	case projectErr == nil:
		options.ProjectLevel, options.StrictCLI = diagnostics.LevelOK, true
		options.Providers = []string{project.Provider}
		options.ProjectNote = fmt.Sprintf("%s provider=%s model=%s state=%s", project.Name, project.Provider, project.Model, project.State)
	case errors.Is(projectErr, store.ErrNotFound):
		options.ProjectNote = "no project registered for this directory (goalforge project init)"
	default:
		return projectErr
	}
	report := diagnostics.Run(ctx, options)
	for _, check := range report.Checks {
		fmt.Printf("%-4s %-16s %s\n", check.Level, check.Name, check.Detail)
	}
	if !report.Ready() {
		return fmt.Errorf("doctor found %d blocking problem(s)", report.Failed)
	}
	fmt.Println("doctor: environment looks ready")
	return nil
}

// mcpServe exposes GoalForge management over the Model Context Protocol:
// stdio by default (for `claude mcp add goalforge -- goalforge mcp`), or the
// Streamable HTTP transport with --addr for remote clients. Binding beyond
// localhost without a bearer token is refused.
func mcpServe(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("mcp", flag.ContinueOnError)
	addr := f.String("addr", "", "serve MCP over Streamable HTTP on this address (empty = stdio)")
	token := f.String("token", os.Getenv("GOALFORGE_MCP_TOKEN"), "bearer token required from HTTP clients")
	if err := f.Parse(args); err != nil {
		return err
	}
	server, err := mcp.New(s, "")
	if err != nil {
		return err
	}
	if *addr == "" {
		return server.Serve(ctx, os.Stdin, os.Stdout)
	}
	host, _, splitErr := net.SplitHostPort(*addr)
	if splitErr != nil {
		host = *addr
	}
	if *token == "" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errors.New("--token (or GOALFORGE_MCP_TOKEN) is required when binding beyond localhost")
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", server.Handler(*token))
	httpServer := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	fmt.Printf("MCP server listening on http://%s/mcp\n", *addr)
	if err = httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// recoverIfFailed returns a FAILED project (and its stuck work item) to a
// runnable state before a deliberate retry; other states pass through.
func recoverIfFailed(ctx context.Context, s *store.Store, projectID string) error {
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		return err
	}
	if project.State != "FAILED" {
		return nil
	}
	return s.RecoverFailedProject(ctx, projectID)
}

func runWorker(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("worker", flag.ContinueOnError)
	once := f.Bool("once", false, "process at most one due job")
	poll := f.Duration("poll", time.Second, "poll interval")
	lease := f.Duration("lease", time.Minute, "scheduler and project lease duration")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *poll <= 0 || *lease <= 0 {
		return errors.New("--poll and --lease must be positive")
	}
	providers, cleanup, err := workerProviders(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	runner, err := orchestrator.New(s, providers...)
	if err != nil {
		return err
	}
	owner := fmt.Sprintf("goalforge-worker-%d", os.Getpid())
	worker, err := scheduler.New(s, owner, *lease)
	if err != nil {
		return err
	}
	if err = worker.Handle("RESUME", runner.ResumeHandler(orchestrator.ResumeConfig{Owner: owner + "-project", LeaseDuration: *lease, Policy: usagepolicy.DefaultPolicy(), Inspector: gitops.GitInspector{}})); err != nil {
		return err
	}
	planning, err := planner.NewService(s, planner.DefaultPolicy())
	if err != nil {
		return err
	}
	verifier, err := verification.New(s, 1024*1024)
	if err != nil {
		return err
	}
	service, err := app.New(s, planning, runner, verifier, nil)
	if err != nil {
		return err
	}
	if err = worker.Handle("CONTINUE", continueHandler(s, service)); err != nil {
		return err
	}
	runOnce := func() (bool, error) { return worker.RunOne(ctx, time.Now().UTC()) }
	if *once {
		ran, runErr := runOnce()
		fmt.Printf("worker: job_processed=%t\n", ran)
		return runErr
	}
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		// SESSION-010: retention pruning rides along with the job pump.
		if now := time.Now().UTC(); now.Sub(lastPrune) >= time.Hour {
			if pruned, pruneErr := s.PruneSessions(ctx, now); pruneErr != nil {
				fmt.Fprintln(os.Stderr, "worker prune error:", pruneErr)
			} else if pruned > 0 {
				fmt.Printf("worker: pruned %d expired sessions\n", pruned)
			}
			lastPrune = now
		}
		ran, runErr := runOnce()
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "worker job error:", runErr)
		}
		if ran {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// continueHandler drives a project toward its goal one work item at a time:
// the CONTINUE job reschedules itself after every verified run, waits out
// quota windows, and stops on completion or anything needing user judgment.
func continueHandler(s *store.Store, service *app.Service) scheduler.Handler {
	return func(ctx context.Context, job store.SchedulerJob) (out scheduler.Outcome, err error) {
		project, err := s.ProjectByID(ctx, job.ProjectID)
		if err != nil {
			return out, err
		}
		rescheduleAfterQuota := func() {
			at := time.Now().UTC().Add(30 * time.Minute)
			if quotas, quotaErr := s.ListQuotaWindows(ctx, project.Provider); quotaErr == nil {
				for _, quota := range quotas {
					if quota.ResumeAt != nil && quota.ResumeAt.After(time.Now().UTC()) {
						value := quota.ResumeAt.Add(2 * time.Minute)
						at = value
					}
				}
			}
			out.RescheduleAt = &at
		}
		switch project.State {
		case "COMPLETED", "CANCELLED":
			return out, nil
		case "BLOCKED", "FAILED":
			return out, fmt.Errorf("project state %s requires user attention", project.State)
		case "WAITING_QUOTA", "RUNNING", "PREFLIGHT", "VERIFYING", "DRAINING", "CHECKPOINTING", "RESUMING":
			rescheduleAfterQuota()
			return out, nil
		}
		result, runErr := service.Continue(ctx, project)
		switch {
		case errors.Is(runErr, orchestrator.ErrWaitingQuota):
			rescheduleAfterQuota()
			return out, nil
		case errors.Is(runErr, store.ErrNotFound):
			// Backlog has no executable work; completion criteria decide the
			// rest, so hand control back to the user.
			return out, nil
		case runErr != nil:
			return out, runErr
		}
		if result.Verification.GoalCompleted {
			return out, nil
		}
		// A failed verification is only retried when the failure is one a code
		// fix could plausibly address and the repair limits still allow it.
		// Rescheduling unconditionally turned a broken environment or an
		// unfixable failure into a loop that spent budget without progress.
		if !result.Verification.Passed {
			plan := result.Repair
			if !plan.Automatic() {
				fmt.Printf("worker: repair stopped run=%s decision=%s reason=%s\n", result.Run.RunID, plan.Decision, plan.Reason)
				_ = notify.Post(ctx, notify.Event{Project: project.ID, State: "REPAIR_REQUIRED", Reason: plan.Decision + ": " + plan.Reason})
				return out, nil
			}
			fmt.Printf("worker: repair scheduled run=%s attempt=%d kind=%s\n", result.Run.RunID, plan.Attempt, plan.FailureKind)
		}
		next := time.Now().UTC().Add(5 * time.Second)
		out.RescheduleAt = &next
		return out, nil
	}
}

// printRepair explains a failed verification: what kind of failure it was and
// whether GoalForge will try again on its own.
func printRepair(plan store.RepairPlan) {
	if plan.Decision == "" || plan.Decision == store.RepairNothingToRepair {
		return
	}
	fmt.Printf("repair: decision=%s kind=%s attempt=%d\n  %s\n  %s\n", plan.Decision, plan.FailureKind, plan.Attempt, plan.Reason, plan.Summary)
}

// verifyIntegration runs the project's gates against the default branch. Work
// items verify inside isolated worktrees, so a merged result has never been
// tested as a whole until this runs.
func verifyIntegration(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("verify integration", flag.ContinueOnError)
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	gates, err := s.ListGates(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(gates) == 0 {
		return errors.New("no verification gates configured")
	}
	engine, err := verification.New(s, 1024*1024)
	if err != nil {
		return err
	}
	checks := make([]verification.Gate, 0, len(gates))
	for _, g := range gates {
		checks = append(checks, verification.Gate{Type: g.Type, Command: g.Command, Timeout: g.Timeout, Required: g.Required, SuccessValue: g.SuccessValue, ValuePattern: g.ValuePattern})
	}
	branchSHA, err := gitops.HeadCommit(ctx, p.RepositoryPath, p.DefaultBranch)
	if err != nil {
		return err
	}
	results, passed, err := engine.Check(ctx, p.RepositoryPath, checks)
	if err != nil {
		return err
	}
	var details []string
	for _, result := range results {
		fmt.Printf("%-8s %-20s exit=%d %s\n", result.Status, result.Type, result.ExitCode, result.FailureSummary)
		if result.Required && result.Status != "PASSED" {
			details = append(details, result.Type+": "+result.Status)
		}
	}
	if err = s.RecordIntegrationResult(ctx, p.ID, branchSHA, strings.Join(details, "; "), passed); err != nil {
		return err
	}
	if !passed {
		return fmt.Errorf("integration verification failed on %s (%s)", p.DefaultBranch, strings.Join(details, "; "))
	}
	fmt.Printf("integration verified: %s at %s\n", p.DefaultBranch, branchSHA)
	return nil
}

// modelAdvice shows how the approved models have actually performed and which
// one would be chosen for the next run, with the reason.
func modelAdvice(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("models", flag.ContinueOnError)
	taskType := f.String("task-type", "CONTINUE_GOAL", "task type to compare (empty for all)")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	stats, err := s.ModelStats(ctx, p.ID, *taskType)
	if err != nil {
		return err
	}
	fmt.Printf("%-24s %6s %8s %12s %10s\n", "model", "runs", "verified", "cost/run", "avg sec")
	for _, stat := range stats {
		fmt.Printf("%-24s %6d %7.0f%% %12.4f %10.0f\n", stat.Model, stat.Runs, stat.SuccessRate, stat.AverageCostPerRun, stat.AverageSeconds)
	}
	choice, err := s.SelectModelForTask(ctx, p.ID, p.Model, p.FallbackModel, *taskType)
	if err != nil {
		return err
	}
	fmt.Printf("\nselected: %s (%s)\n  %s\n", choice.Model, choice.Source, choice.Reason)
	forecast, err := s.ForecastTokens(ctx, p.ID, *taskType)
	if err != nil {
		return err
	}
	fmt.Printf("forecast: %d tokens (range %d~%d, samples %d, confidence %s)\n  %s\n",
		forecast.Expected, forecast.Low, forecast.High, forecast.Samples, forecast.Confidence, forecast.Basis)
	accuracy, err := s.EstimateAccuracy(ctx, p.ID)
	if err != nil {
		return err
	}
	if accuracy.Samples > 0 {
		fmt.Printf("estimate error: %.0f%% mean absolute over %d runs (over %d / under %d)\n",
			accuracy.MeanAbsolutePercent, accuracy.Samples, accuracy.Overestimates, accuracy.Underestimates)
	}
	return nil
}

// activityReport summarizes what ran while nobody was watching: what
// finished, what stopped and why, what is waiting on a decision, and the cost.
func activityReport(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("report", flag.ContinueOnError)
	since := f.Duration("since", 24*time.Hour, "window to summarize")
	asJSON := f.Bool("json", false, "emit JSON instead of text")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *since <= 0 {
		return errors.New("--since must be positive")
	}
	report, err := s.Activity(ctx, time.Now().UTC().Add(-*since))
	if err != nil {
		return err
	}
	if *asJSON {
		encoded, encodeErr := json.MarshalIndent(report, "", "  ")
		if encodeErr != nil {
			return encodeErr
		}
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("GoalForge report: %s ~ %s (%s)\n", report.Since.Format(time.RFC3339), report.Until.Format(time.RFC3339), *since)
	fmt.Printf("runs=%d work_verified=%d tokens=%d cost_usd=%.4f\n\n", report.Runs, report.WorkCompleted, report.Tokens, report.CostUSD)
	for _, project := range report.Projects {
		if project.Runs == 0 && project.WorkCompleted == 0 {
			continue
		}
		fmt.Printf("%-20s %-16s runs=%d verified=%d progress=%.1f%% cost=$%.4f  %s\n",
			project.Name, project.State, project.Runs, project.WorkCompleted, project.ProgressPercent, project.CostUSD, project.GoalTitle)
	}
	if len(report.Unresolved) > 0 {
		fmt.Printf("\nunresolved (%d):\n", len(report.Unresolved))
		for _, item := range report.Unresolved {
			fmt.Printf("  %-12s %-20s %s %s\n    %s\n", item.State, item.ProjectName, item.RunID, item.FailureKind, item.Reason)
		}
	}
	if len(report.Approvals) > 0 {
		fmt.Printf("\napprovals waiting (%d):\n", len(report.Approvals))
		for _, approval := range report.Approvals {
			fmt.Printf("  %-20s %-22s %s  %s\n", approval.ProjectName, approval.ActionType, approval.ID, approval.Reason)
		}
	}
	if len(report.Unresolved) == 0 && len(report.Approvals) == 0 {
		fmt.Println("\nnothing is waiting on you.")
	}
	return nil
}

func workerProviders(ctx context.Context) ([]provider.Provider, func(), error) {
	cleanup := func() {}
	var codexProvider provider.Provider = codex.New(os.Getenv("GOALFORGE_CODEX_BIN"))
	if os.Getenv("GOALFORGE_CODEX_TRANSPORT") == "app-server" {
		adapter, err := codex.StartAppServerAdapter(ctx, "")
		if err != nil {
			return nil, cleanup, err
		}
		codexProvider = adapter
		cleanup = func() { _ = adapter.Close() }
	}
	return []provider.Provider{
		codexProvider,
		claude.New(os.Getenv("GOALFORGE_CLAUDE_BIN")),
		qwen.New(os.Getenv("GOALFORGE_QWEN_BIN")),
		opencode.New(os.Getenv("GOALFORGE_OPENCODE_BIN")),
	}, cleanup, nil
}

func approvalRequest(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("approval request", flag.ContinueOnError)
	action := f.String("action", "protected-files", "protected-files, publish-branch, or merge-branch")
	reason := f.String("reason", "", "reason for approval")
	workItemID := f.String("work-item", "", "work item whose verified commit is being approved (required for publish-branch and merge-branch)")
	remote := f.String("remote", "origin", "git remote the publish approval applies to")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *reason == "" {
		return errors.New("--reason is required")
	}
	actionType := ""
	switch *action {
	case "protected-files", store.ApprovalProtectedFiles:
		actionType = store.ApprovalProtectedFiles
	case "publish-branch", store.ApprovalPublishBranch:
		actionType = store.ApprovalPublishBranch
	case "merge-branch", store.ApprovalMergeBranch:
		actionType = store.ApprovalMergeBranch
	default:
		return fmt.Errorf("unsupported approval action %q", *action)
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	scope, err := approvalScope(ctx, s, p, actionType, *workItemID, *remote)
	if err != nil {
		return err
	}
	approval, err := s.RequestScopedApproval(ctx, p.ID, actionType, *reason, scope)
	if err != nil {
		return err
	}
	if approval.Scope.Scoped() {
		fmt.Printf("approval requested: %s action=%s work=%s commit=%s branch=%s target=%s files=%d\n",
			approval.ID, approval.ActionType, scope.WorkItemID, shortSHA(scope.CommitSHA), scope.SourceBranch, scope.TargetRef, scope.FilesChanged)
		return nil
	}
	fmt.Printf("approval requested: %s action=%s\n", approval.ID, approval.ActionType)
	return nil
}

// approvalScope resolves what a publish or merge approval actually covers, so
// the reviewer approves a named commit instead of an action type. Resolving it
// at request time is also what makes a later commit detectable as a change the
// approval no longer covers.
func approvalScope(ctx context.Context, s *store.Store, p model.Project, actionType, workItemID, remote string) (store.ApprovalScope, error) {
	if actionType != store.ApprovalMergeBranch && actionType != store.ApprovalPublishBranch {
		if workItemID != "" {
			return store.ApprovalScope{}, fmt.Errorf("--work-item does not apply to %s approvals", actionType)
		}
		return store.ApprovalScope{}, nil
	}
	if workItemID == "" {
		return store.ApprovalScope{}, fmt.Errorf("--work-item is required for %s approvals so the approval names the commit being approved", actionType)
	}
	commit, err := s.LatestRunCommitForWork(ctx, p.ID, workItemID)
	if errors.Is(err, store.ErrNotFound) {
		return store.ApprovalScope{}, fmt.Errorf("work item %s has no verified commit yet; only verified work can be approved", workItemID)
	}
	if err != nil {
		return store.ApprovalScope{}, err
	}
	scope := store.ApprovalScope{WorkItemID: workItemID, SourceBranch: commit.Branch, CommitSHA: commit.CommitSHA, FilesChanged: commit.FilesCommitted}
	if actionType == store.ApprovalMergeBranch {
		scope.TargetRef = p.DefaultBranch
	} else {
		scope.TargetRef = remote
	}
	return scope, nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func approvalApprove(ctx context.Context, s *store.Store, args []string) error {
	if len(args) != 1 {
		return errors.New("approval approve requires an approval ID")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if err = s.Approve(ctx, p.ID, args[0]); err != nil {
		return err
	}
	fmt.Printf("approval granted: %s\n", args[0])
	return nil
}

func approvalReject(ctx context.Context, s *store.Store, args []string) error {
	if len(args) != 1 {
		return errors.New("approval reject requires an approval ID")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if err = s.RejectApproval(ctx, p.ID, args[0]); err != nil {
		return err
	}
	fmt.Printf("approval rejected: %s\n", args[0])
	return nil
}

func usageShow(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	// Actual usage comes from the ledger and must be visible even when no
	// budget was configured.
	metrics, err := s.ProjectMetrics(ctx, p.ID)
	if err != nil {
		return err
	}
	total := metrics.InputTokens + metrics.OutputTokens + metrics.CachedInputTokens + metrics.ReasoningTokens
	fmt.Printf("tokens used: input=%d output=%d cached=%d reasoning=%d total=%d\ncost used: %.4f USD\n", metrics.InputTokens, metrics.OutputTokens, metrics.CachedInputTokens, metrics.ReasoningTokens, total, metrics.CostUSD)
	budget, err := s.ProjectBudgetUsage(ctx, p.ID)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Println("budget: not set (goalforge project budget --tokens ... --cost-usd ...)")
	} else if err != nil {
		return err
	} else {
		fmt.Printf("token budget: %d / %d\ncost budget: %.4f / %.4f USD\n", budget.TokensUsed, budget.TokenLimit, budget.CostUsedUSD, budget.CostLimitUSD)
	}
	quotas, err := s.ListQuotaWindows(ctx, p.Provider)
	if err != nil {
		return err
	}
	for _, q := range quotas {
		reset, resume := "unknown", "unknown"
		if q.QuotaResetAt != nil {
			reset = q.QuotaResetAt.Local().Format(time.RFC3339)
		}
		if q.ResumeAt != nil {
			resume = q.ResumeAt.Local().Format(time.RFC3339)
		}
		fmt.Printf("quota: %s/%s status=%s used=%.1f%% reset=%s resume=%s source=%s confidence=%s\n", q.AccountID, q.LimitType, q.Status, q.UsedPercent, reset, resume, q.Source, q.Confidence)
	}
	return nil
}

func sessionsShow(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	sessions, err := s.ListSessions(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		fmt.Printf("%s\t%s\t%s\t%s\n", session.Provider, session.Status, session.SessionID, session.LastRunID)
	}
	return nil
}

func checkpointCreate(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("checkpoint", flag.ContinueOnError)
	next := f.String("next-action", "review the current goal and select the next runnable work item", "specific next action")
	completed := f.String("completed", "manual checkpoint", "completed work summary")
	remaining := f.String("remaining", "", "remaining steps")
	risks := f.String("risks", "", "unresolved risks")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	goal, err := s.CurrentGoal(ctx, p.ID)
	if err != nil {
		return err
	}
	snapshot, err := (gitops.GitInspector{}).Snapshot(ctx, p.RepositoryPath)
	if err != nil {
		return err
	}
	cp := store.Checkpoint{ProjectID: p.ID, GoalVersion: goal.Version, Provider: p.Provider, Model: p.Model, CommitSHA: snapshot.CommitSHA, Branch: snapshot.Branch, DirtyFiles: snapshot.DirtyFiles, DirtyFingerprint: snapshot.DirtyFingerprint, CompletedSummary: *completed, RemainingSteps: *remaining, NextAction: *next, RiskSummary: *risks}
	if session, sessionErr := s.ActiveSession(ctx, p.ID, p.Provider); sessionErr == nil {
		cp.SessionID = session.SessionID
	} else if !errors.Is(sessionErr, store.ErrNotFound) {
		return sessionErr
	}
	cp, err = s.CreateCheckpoint(ctx, cp)
	if err != nil {
		return err
	}
	fmt.Printf("checkpoint created: %s commit=%s dirty_files=%d\ncontinuity: %s\n", cp.ID, cp.CommitSHA, len(cp.DirtyFiles), s.ContinuityPath(p.ID))
	return nil
}

func logsShow(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("logs", flag.ContinueOnError)
	limit := f.Int("limit", 50, "maximum events (1-1000)")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	events, err := s.ListEventLogs(ctx, p.ID, *limit)
	if err != nil {
		return err
	}
	for _, event := range events {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", event.CreatedAt.Local().Format(time.RFC3339), event.RunID, event.Provider, event.Type, event.Raw)
	}
	return nil
}

func cancelScheduled(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	control, err := s.RequestRunControl(ctx, p.ID, "CANCEL")
	if err == nil {
		fmt.Printf("cancel requested: run=%s request=%s\n", control.RunID, control.ID)
		return nil
	}
	if !errors.Is(err, store.ErrNoRunningExecution) {
		return err
	}
	count, err := s.CancelProjectJobs(ctx, p.ID)
	if err != nil {
		return err
	}
	fmt.Printf("cancelled scheduled jobs: %d\n", count)
	return nil
}

func pauseExecution(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	control, err := s.RequestRunControl(ctx, p.ID, "PAUSE")
	if err != nil {
		return err
	}
	fmt.Printf("pause requested: run=%s request=%s\n", control.RunID, control.ID)
	return nil
}

func resumePaused(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	service, cleanup, err := runtimeService(ctx, s, p)
	if err != nil {
		return err
	}
	defer cleanup()
	result, err := service.ResumePaused(ctx, p)
	if err != nil {
		return err
	}
	fmt.Printf("resumed checkpoint: %s\nrun: %s state=%s resumed=%t\nverification: passed=%t goal_completed=%t progress=%.1f%%\n", result.Checkpoint.ID, result.Run.RunID, result.Run.State, result.Run.Resumed, result.Verification.Passed, result.Verification.GoalCompleted, result.Verification.Progress)
	printRepair(result.Repair)
	return nil
}

func projectBudget(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("project budget", flag.ContinueOnError)
	tokens := f.Int64("tokens", 0, "project token limit")
	cost := f.Float64("cost-usd", 0, "project cost limit")
	dailyRuns := f.Int64("daily-runs", 0, "daily run limit (UTC day)")
	dailyTokens := f.Int64("daily-tokens", 0, "daily token limit (UTC day)")
	dailyCost := f.Float64("daily-cost-usd", 0, "daily cost limit (UTC day)")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if err = s.SetProjectBudget(ctx, p.ID, *tokens, *cost); err != nil {
		return err
	}
	if err = s.SetDailyLimits(ctx, p.ID, *dailyRuns, *dailyTokens, *dailyCost); err != nil {
		return err
	}
	fmt.Printf("project budget set: tokens=%d cost_usd=%.2f daily_runs=%d daily_tokens=%d daily_cost_usd=%.2f\n", *tokens, *cost, *dailyRuns, *dailyTokens, *dailyCost)
	return nil
}

func gateAdd(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("verify gate add", flag.ContinueOnError)
	kind := f.String("type", "", "criterion type")
	commandJSON := f.String("command-json", "", "JSON command array")
	timeout := f.Int("timeout-seconds", 300, "timeout seconds")
	optional := f.Bool("optional", false, "non-blocking gate")
	success := f.String("success-value", "true", "criterion value on success (numeric values become a minimum threshold)")
	valuePattern := f.String("value-pattern", "", "regular expression with one capture group extracting the measured value from the gate output")
	if err := f.Parse(args); err != nil {
		return err
	}
	var command []string
	if err := json.Unmarshal([]byte(*commandJSON), &command); err != nil {
		return fmt.Errorf("decode --command-json: %w", err)
	}
	if err := policy.ValidateCommand(command); err != nil {
		return fmt.Errorf("verification gate command rejected: %w", err)
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if err = s.UpsertGate(ctx, p.ID, store.GateConfig{Type: *kind, Command: command, Timeout: time.Duration(*timeout) * time.Second, Required: !*optional, SuccessValue: *success, ValuePattern: *valuePattern}); err != nil {
		return err
	}
	if *valuePattern != "" {
		fmt.Printf("verification gate configured: %s %v measured=%s threshold=%s\n", *kind, command, *valuePattern, *success)
		return nil
	}
	fmt.Printf("verification gate configured: %s %v\n", *kind, command)
	return nil
}

func continueGoal(ctx context.Context, s *store.Store, args []string, developSelected bool) error {
	f := flag.NewFlagSet("continue", flag.ContinueOnError)
	enqueue := f.Bool("enqueue", false, "schedule a persistent CONTINUE job for the worker instead of running inline")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if *enqueue {
		job, jobErr := s.ScheduleRecurringJob(ctx, store.SchedulerJob{ProjectID: p.ID, Type: "CONTINUE", RunAt: time.Now().UTC(), IdempotencyKey: "continue:" + p.ID})
		if jobErr != nil {
			return jobErr
		}
		fmt.Printf("continue job scheduled: %s (run `goalforge worker` to process it)\n", job.ID)
		return nil
	}
	service, cleanup, err := runtimeService(ctx, s, p)
	if err != nil {
		return err
	}
	defer cleanup()
	execute := service.Continue
	if developSelected {
		execute = service.Develop
	}
	result, err := execute(ctx, p)
	if err != nil {
		return err
	}
	fmt.Printf("work item: %s %s\nrun: %s state=%s resumed=%t\nverification: passed=%t goal_completed=%t progress=%.1f%%\n", result.WorkItem.ID, result.WorkItem.Title, result.Run.RunID, result.Run.State, result.Run.Resumed, result.Verification.Passed, result.Verification.GoalCompleted, result.Verification.Progress)
	printRepair(result.Repair)
	return nil
}

func ideasGoal(ctx context.Context, s *store.Store) error {
	return discoverGoal(ctx, s, false)
}

func replanGoal(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	service, cleanup, err := runtimeService(ctx, s, p)
	if err != nil {
		return err
	}
	defer cleanup()
	result, err := service.Replan(ctx, p)
	if err != nil {
		return err
	}
	fmt.Printf("run: %s state=%s\n", result.Run.RunID, result.Run.State)
	for _, stale := range result.Stale {
		status := "flagged"
		if !stale.Applied {
			status = "skipped (" + stale.Note + ")"
		}
		fmt.Printf("stale\t%s\t%s\t%s\n", status, stale.ID, stale.Reason)
	}
	for _, gap := range result.Discovery.Accepted {
		fmt.Printf("gap\t%s\t%.2f\t%s\t%s\n", gap.Status, gap.Score.PriorityScore, gap.Candidate.Risk, gap.Candidate.Title)
	}
	for title, reason := range result.Discovery.Rejected {
		fmt.Printf("rejected\t%s\t%s\n", reason, title)
	}
	return nil
}

func auditGoal(ctx context.Context, s *store.Store) error {
	return discoverGoal(ctx, s, true)
}

func discoverGoal(ctx context.Context, s *store.Store, audit bool) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	service, cleanup, err := runtimeService(ctx, s, p)
	if err != nil {
		return err
	}
	defer cleanup()
	discover := service.Ideas
	if audit {
		discover = service.Audit
	}
	result, err := discover(ctx, p)
	if err != nil {
		return err
	}
	fmt.Printf("run: %s state=%s\n", result.Run.RunID, result.Run.State)
	for _, idea := range result.Discovery.Accepted {
		fmt.Printf("accepted\t%s\t%.2f\t%s\t%s\n", idea.Status, idea.Score.PriorityScore, idea.Candidate.Risk, idea.Candidate.Title)
	}
	for title, reason := range result.Discovery.Rejected {
		fmt.Printf("rejected\t%s\t%s\n", reason, title)
	}
	return nil
}

func runtimeService(ctx context.Context, s *store.Store, p model.Project) (*app.Service, func(), error) {
	var selected provider.Provider
	cleanup := func() {}
	switch p.Provider {
	case "codex":
		if os.Getenv("GOALFORGE_CODEX_TRANSPORT") == "app-server" {
			adapter, err := codex.StartAppServerAdapter(ctx, "")
			if err != nil {
				return nil, cleanup, err
			}
			selected = adapter
			cleanup = func() { _ = adapter.Close() }
		} else {
			selected = codex.New(os.Getenv("GOALFORGE_CODEX_BIN"))
		}
	case "claude":
		selected = claude.New(os.Getenv("GOALFORGE_CLAUDE_BIN"))
	case "qwen":
		selected = qwen.New(os.Getenv("GOALFORGE_QWEN_BIN"))
	case "opencode":
		selected = opencode.New(os.Getenv("GOALFORGE_OPENCODE_BIN"))
	default:
		return nil, cleanup, fmt.Errorf("unsupported provider %q", p.Provider)
	}
	planning, err := planner.NewService(s, planner.DefaultPolicy())
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	runner, err := orchestrator.New(s, selected)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	verifier, err := verification.New(s, 1024*1024)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	service, err := app.New(s, planning, runner, verifier, nil)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return service, cleanup, nil
}

func activeGoal(ctx context.Context, s *store.Store) (model.Goal, error) {
	p, err := currentProject(ctx, s)
	if err != nil {
		return model.Goal{}, err
	}
	return s.CurrentGoal(ctx, p.ID)
}

func milestoneAdd(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("milestone add", flag.ContinueOnError)
	title := f.String("title", "", "title")
	weight := f.Float64("weight", 1, "weight")
	if err := f.Parse(args); err != nil {
		return err
	}
	g, err := activeGoal(ctx, s)
	if err != nil {
		return err
	}
	m, err := s.CreateMilestone(ctx, model.Milestone{GoalID: g.ID, Title: *title, Weight: *weight})
	if err != nil {
		return err
	}
	fmt.Printf("milestone added: %s %s\n", m.ID, m.Title)
	return nil
}

func workAdd(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("work add", flag.ContinueOnError)
	title := f.String("title", "", "title")
	kind := f.String("type", "IMPLEMENT", "type")
	milestone := f.String("milestone", "", "milestone ID")
	dependency := f.String("depends-on", "", "comma-separated work IDs that must be DONE first")
	priority := f.Float64("priority", 0, "priority")
	weight := f.Float64("weight", 1, "weight")
	risk := f.String("risk", "medium", "risk")
	estimatedTokens := f.Int64("estimated-tokens", 0, "estimated tokens for one AI run")
	changeScope := f.String("scope", "", "allowed file path prefix or glob (comma-separated)")
	if err := f.Parse(args); err != nil {
		return err
	}
	g, err := activeGoal(ctx, s)
	if err != nil {
		return err
	}
	if *estimatedTokens < 0 {
		return errors.New("--estimated-tokens must be non-negative")
	}
	w, err := s.CreateWorkItem(ctx, model.WorkItem{GoalID: g.ID, MilestoneID: *milestone, Type: *kind, Title: *title, Priority: *priority, Dependencies: splitList(*dependency), Risk: *risk, ChangeScope: *changeScope, Weight: *weight, EstimatedTokens: *estimatedTokens})
	if err != nil {
		return err
	}
	fmt.Printf("work item added: %s %s\n", w.ID, w.Title)
	return nil
}

func workList(ctx context.Context, s *store.Store) error {
	g, err := activeGoal(ctx, s)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Println("no active goal for this project (the goal may be completed; set a new one with `goalforge goal set`)")
		return nil
	}
	if err != nil {
		return err
	}
	items, err := s.ListWorkItems(ctx, g.ID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("backlog is empty (add work with `goalforge work add` or discover with `goalforge ideas`)")
		return nil
	}
	for _, w := range items {
		fmt.Printf("%s\t%s\t%.2f\t%s\n", w.ID, w.Status, w.Priority, w.Title)
	}
	return nil
}

func workStatus(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("work status", flag.ContinueOnError)
	status := f.String("set", "", "new status")
	// The documented form puts the ID first, and Go's flag package stops
	// parsing at the first non-flag argument, so the ID is taken out before
	// the flags are parsed instead of being silently ignored.
	workID, rest := splitLeadingArg(args)
	if err := f.Parse(rest); err != nil {
		return err
	}
	if workID == "" && f.NArg() == 1 {
		workID = f.Arg(0)
	}
	if workID == "" || *status == "" {
		return errors.New("work status requires ID and --set STATUS")
	}
	g, err := activeGoal(ctx, s)
	if err != nil {
		return err
	}
	if err := s.SetWorkItemStatus(ctx, g.ID, workID, strings.ToUpper(*status)); err != nil {
		return err
	}
	fmt.Printf("work item updated: %s %s\n", workID, strings.ToUpper(*status))
	return nil
}

// splitLeadingArg pulls a leading positional argument off a command line so the
// remaining flags parse normally.
// splitList parses a comma-separated flag value, ignoring empty entries.
func splitList(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func splitLeadingArg(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func verifyRecord(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("verify record", flag.ContinueOnError)
	check := f.String("check", "", "criterion type")
	status := f.String("status", "", "PASSED, FAILED, or UNKNOWN")
	actual := f.String("actual", "", "actual value")
	output := f.String("output", "", "evidence")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *check == "" || *status == "" {
		return errors.New("--check and --status are required")
	}
	g, err := activeGoal(ctx, s)
	if err != nil {
		return err
	}
	if err := s.RecordVerification(ctx, g.ID, *check, strings.ToUpper(*status), *actual, *output); err != nil {
		return err
	}
	fmt.Printf("verification recorded: %s %s\n", *check, strings.ToUpper(*status))
	return nil
}

func projectInit(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("project init", flag.ContinueOnError)
	name := f.String("name", "", "project name")
	repo := f.String("repo", ".", "repository path")
	branch := f.String("branch", "", "default branch")
	provider := f.String("provider", "codex", "provider: codex, claude, qwen, or opencode")
	modelName := f.String("model", "", "model")
	fallbackModel := f.String("fallback-model", "", "approved substitute model when the configured model is rejected")
	worktreeEnabled := f.Bool("worktrees", false, "run each work item in a dedicated Git worktree")
	autoCommit := f.Bool("auto-commit", false, "commit verified changes with Goal/Work-Item trailers after gates pass")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	if !isSupportedProvider(*provider) {
		return fmt.Errorf("--provider must be one of %s", strings.Join(supportedProviders, ", "))
	}
	abs, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(abs, ".git")); err != nil {
		return fmt.Errorf("repository is not a Git repository: %s", abs)
	}
	if *branch == "" {
		out, e := exec.CommandContext(ctx, "git", "-C", abs, "branch", "--show-current").Output()
		if e != nil {
			return fmt.Errorf("read Git branch: %w", e)
		}
		*branch = strings.TrimSpace(string(out))
		if *branch == "" {
			*branch = "main"
		}
	}
	p := model.Project{Name: *name, RepositoryPath: abs, DefaultBranch: *branch, Provider: *provider, Model: *modelName, FallbackModel: *fallbackModel, WorktreeEnabled: *worktreeEnabled, AutoCommitEnabled: *autoCommit}
	if err = s.CreateProject(ctx, p); err != nil {
		return err
	}
	fmt.Printf("project registered: %s (%s)\n", *name, abs)
	return nil
}

func currentProject(ctx context.Context, s *store.Store) (model.Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return model.Project{}, err
	}
	return s.ProjectByPath(ctx, cwd)
}

func goalSet(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("goal set", flag.ContinueOnError)
	title := f.String("title", "", "goal title")
	objective := f.String("objective", "", "objective")
	reason := f.String("reason", "", "change reason")
	var raw listFlag
	f.Var(&raw, "criterion", "key=value completion criterion")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *title == "" || *objective == "" {
		return errors.New("--title and --objective are required")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return fmt.Errorf("find registered project: %w", err)
	}
	criteria := make([]model.Criterion, 0, len(raw))
	for _, v := range raw {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return fmt.Errorf("invalid criterion %q; use key=value", v)
		}
		criteria = append(criteria, model.Criterion{Type: parts[0], ExpectedValue: parts[1]})
	}
	if len(criteria) == 0 {
		return errors.New("at least one --criterion is required")
	}
	g, err := s.SetGoal(ctx, p.ID, *title, *objective, *reason, criteria)
	if err != nil {
		return err
	}
	fmt.Printf("goal set: %s v%d\n", g.Title, g.Version)
	return nil
}

func goalShow(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return fmt.Errorf("find registered project: %w", err)
	}
	g, err := s.CurrentGoal(ctx, p.ID)
	if errors.Is(err, store.ErrNotFound) {
		g, err = s.LatestGoal(ctx, p.ID)
	}
	if err != nil {
		return fmt.Errorf("find active goal: %w", err)
	}
	progress, complete, err := s.GoalProgress(ctx, g)
	if err != nil {
		return err
	}
	metrics, err := s.ProjectMetrics(ctx, p.ID)
	if err != nil {
		return err
	}
	verificationRate := float64(0)
	if metrics.VerificationTotal > 0 {
		verificationRate = float64(metrics.VerificationPassed) / float64(metrics.VerificationTotal) * 100
	}
	fmt.Printf("Project: %s\nGoal: %s (v%d, %s)\nState: %s\nProgress: %.1f%%\nCompletion verified: %t\nRuns: total=%d provider_success=%d failed=%d avg_seconds=%.2f\nWork: done=%d blocked=%d\nVerification: passed=%d total=%d rate=%.1f%%\nSessions: %d\nTokens: input=%d output=%d cached=%d reasoning=%d cost_usd=%.4f\nCriteria:\n", p.Name, g.Title, g.Version, g.Status, p.State, progress, complete, metrics.RunsTotal, metrics.RunsSuccessful, metrics.RunsFailed, metrics.AverageRunSeconds, metrics.WorkDone, metrics.WorkBlocked, metrics.VerificationPassed, metrics.VerificationTotal, verificationRate, metrics.SessionCount, metrics.InputTokens, metrics.OutputTokens, metrics.CachedInputTokens, metrics.ReasoningTokens, metrics.CostUSD)
	for _, c := range g.Criteria {
		fmt.Printf("  - %s = %s\n", c.Type, c.ExpectedValue)
	}
	return nil
}
