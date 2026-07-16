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
	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/orchestrator"
	"github.com/goalforge/goalforge/internal/planner"
	"github.com/goalforge/goalforge/internal/policy"
	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/provider/claude"
	"github.com/goalforge/goalforge/internal/provider/codex"
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
	case "continue":
		return continueGoal(ctx, s, false)
	case "develop":
		return continueGoal(ctx, s, true)
	case "ideas":
		return ideasGoal(ctx, s)
	case "usage":
		return usageShow(ctx, s)
	case "sessions":
		return sessionsShow(ctx, s)
	case "checkpoint":
		return checkpointCreate(ctx, s, args[1:])
	case "logs":
		return logsShow(ctx, s, args[1:])
	case "cancel":
		return cancelScheduled(ctx, s)
	case "pause":
		return pauseExecution(ctx, s)
	case "resume":
		return resumePaused(ctx, s)
	case "rollback":
		return rollbackWork(ctx, s, args[1:])
	case "approval":
		if len(args) > 1 && args[1] == "request" {
			return approvalRequest(ctx, s, args[2:])
		}
	case "worker":
		return runWorker(ctx, s, args[1:])
	case "serve":
		return serveAPI(ctx, s, args[1:])
	case "run":
		if len(args) > 1 && args[1] == "--until-quota" {
			return runUntilQuota(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "approve" {
			return approvalApprove(ctx, s, args[2:])
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
	return errors.New("usage: goalforge [--db PATH] project init|project budget|project runtime|project provider set|goal set|goal show|milestone add|work add|work list|work status ID|verify gate add|ideas|continue|develop|run --until-quota|status|usage|sessions|checkpoint|logs|pause|resume|rollback|cancel|approval request|approval approve ID|worker [--once]|serve")
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

func projectProviderSet(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("project provider set", flag.ContinueOnError)
	providerName := f.String("provider", "", "target provider: codex or claude")
	modelName := f.String("model", "", "target model")
	reason := f.String("reason", "provider transition requested by user", "handoff reason")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *providerName != "codex" && *providerName != "claude" {
		return errors.New("--provider must be codex or claude")
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
			decision := policy.DecideRetry(kind, failures, 3, nil, rand.Float64)
			if decision.Action == policy.WaitQuotaReset {
				fmt.Printf("quota exhausted (%s); stopping until reset\n", kind)
				return runErr
			}
			if decision.Action != policy.RetryAfterDelay {
				return fmt.Errorf("%s: %w", decision.Reason, runErr)
			}
			fmt.Printf("retryable failure (%s): %v; retrying in %s\n", kind, runErr, decision.Delay.Round(time.Second))
			if waitErr := policy.WaitForRetry(ctx, decision); waitErr != nil {
				return errors.Join(runErr, waitErr)
			}
			continue
		}
		failures = 0
		fmt.Printf("run %d: work=%s state=%s verified=%t progress=%.1f%%\n", index+1, result.WorkItem.ID, result.Run.State, result.Verification.Passed, result.Verification.Progress)
		if result.Verification.GoalCompleted {
			return nil
		}
	}
	return fmt.Errorf("maximum consecutive run limit reached: %d", *maxRuns)
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
	runOnce := func() (bool, error) { return worker.RunOne(ctx, time.Now().UTC()) }
	if *once {
		ran, runErr := runOnce()
		fmt.Printf("worker: job_processed=%t\n", ran)
		return runErr
	}
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
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

func workerProviders(ctx context.Context) ([]provider.Provider, func(), error) {
	cleanup := func() {}
	var codexProvider provider.Provider = codex.New("")
	if os.Getenv("GOALFORGE_CODEX_TRANSPORT") == "app-server" {
		adapter, err := codex.StartAppServerAdapter(ctx, "")
		if err != nil {
			return nil, cleanup, err
		}
		codexProvider = adapter
		cleanup = func() { _ = adapter.Close() }
	}
	return []provider.Provider{codexProvider, claude.New("")}, cleanup, nil
}

func approvalRequest(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("approval request", flag.ContinueOnError)
	action := f.String("action", "protected-files", "protected-files")
	reason := f.String("reason", "", "reason for approval")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *reason == "" {
		return errors.New("--reason is required")
	}
	if *action != "protected-files" {
		return fmt.Errorf("unsupported approval action %q", *action)
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	approval, err := s.RequestApproval(ctx, p.ID, store.ApprovalProtectedFiles, *reason)
	if err != nil {
		return err
	}
	fmt.Printf("approval requested: %s action=%s\n", approval.ID, approval.ActionType)
	return nil
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

func usageShow(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	budget, err := s.ProjectBudgetUsage(ctx, p.ID)
	if errors.Is(err, store.ErrNotFound) {
		budget = store.ProjectBudget{}
	} else if err != nil {
		return err
	}
	fmt.Printf("project tokens: %d / %d\nproject cost: %.4f / %.4f USD\n", budget.TokensUsed, budget.TokenLimit, budget.CostUsedUSD, budget.CostLimitUSD)
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
	fmt.Printf("checkpoint created: %s commit=%s dirty_files=%d\n", cp.ID, cp.CommitSHA, len(cp.DirtyFiles))
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
	success := f.String("success-value", "true", "criterion value on success")
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
	if err = s.UpsertGate(ctx, p.ID, store.GateConfig{Type: *kind, Command: command, Timeout: time.Duration(*timeout) * time.Second, Required: !*optional, SuccessValue: *success}); err != nil {
		return err
	}
	fmt.Printf("verification gate configured: %s %v\n", *kind, command)
	return nil
}

func continueGoal(ctx context.Context, s *store.Store, developSelected bool) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
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
	return nil
}

func ideasGoal(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	service, cleanup, err := runtimeService(ctx, s, p)
	if err != nil {
		return err
	}
	defer cleanup()
	result, err := service.Ideas(ctx, p)
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
			selected = codex.New("")
		}
	case "claude":
		selected = claude.New("")
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
	dependency := f.String("depends-on", "", "dependency work ID")
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
	w, err := s.CreateWorkItem(ctx, model.WorkItem{GoalID: g.ID, MilestoneID: *milestone, Type: *kind, Title: *title, Priority: *priority, Dependency: *dependency, Risk: *risk, ChangeScope: *changeScope, Weight: *weight, EstimatedTokens: *estimatedTokens})
	if err != nil {
		return err
	}
	fmt.Printf("work item added: %s %s\n", w.ID, w.Title)
	return nil
}

func workList(ctx context.Context, s *store.Store) error {
	g, err := activeGoal(ctx, s)
	if err != nil {
		return err
	}
	items, err := s.ListWorkItems(ctx, g.ID)
	if err != nil {
		return err
	}
	for _, w := range items {
		fmt.Printf("%s\t%s\t%.2f\t%s\n", w.ID, w.Status, w.Priority, w.Title)
	}
	return nil
}

func workStatus(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("work status", flag.ContinueOnError)
	status := f.String("set", "", "new status")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 1 || *status == "" {
		return errors.New("work status requires ID and --set STATUS")
	}
	g, err := activeGoal(ctx, s)
	if err != nil {
		return err
	}
	if err := s.SetWorkItemStatus(ctx, g.ID, f.Arg(0), strings.ToUpper(*status)); err != nil {
		return err
	}
	fmt.Printf("work item updated: %s %s\n", f.Arg(0), strings.ToUpper(*status))
	return nil
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
	provider := f.String("provider", "codex", "provider")
	modelName := f.String("model", "", "model")
	worktreeEnabled := f.Bool("worktrees", false, "run each work item in a dedicated Git worktree")
	autoCommit := f.Bool("auto-commit", false, "commit verified changes with Goal/Work-Item trailers after gates pass")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
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
	p := model.Project{Name: *name, RepositoryPath: abs, DefaultBranch: *branch, Provider: *provider, Model: *modelName, WorktreeEnabled: *worktreeEnabled, AutoCommitEnabled: *autoCommit}
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
