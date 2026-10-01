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
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goalforge/goalforge/internal/api"
	"github.com/goalforge/goalforge/internal/app"
	"github.com/goalforge/goalforge/internal/audit"
	"github.com/goalforge/goalforge/internal/diagnostics"
	"github.com/goalforge/goalforge/internal/evaluation"
	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/mcp"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/notify"
	"github.com/goalforge/goalforge/internal/observer"
	"github.com/goalforge/goalforge/internal/orchestrator"
	"github.com/goalforge/goalforge/internal/planner"
	"github.com/goalforge/goalforge/internal/policy"
	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/provider/claude"
	"github.com/goalforge/goalforge/internal/provider/codex"
	"github.com/goalforge/goalforge/internal/provider/opencode"
	"github.com/goalforge/goalforge/internal/provider/qwen"
	"github.com/goalforge/goalforge/internal/report"
	"github.com/goalforge/goalforge/internal/scheduler"
	pgstore "github.com/goalforge/goalforge/internal/store/postgres"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/tui"
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

// usageText lists every command the dispatch actually accepts. It is grouped
// rather than run together because a single line of forty commands is a list
// nobody reads, and commands nobody can find are commands that do not exist.
const usageText = `usage: goalforge [--db PATH] COMMAND

setup      project init | project budget | project runtime | project concurrency | project profile
           project sandbox [--mode docker --image IMG]
           project provider set | project relocate [--name N] | doctor [--probe-auth]
goal       goal set | goal show | goal contract | goal contract show | milestone add | decision add | decision list | decision supersede
work       work add | work list | work status ID --set STATUS
verify     verify template NAME | verify gate add | verify record | verify integration
run        plan [--json] | continue [--enqueue] | develop | run --until-quota | ideas | audit | replan
           worker [--once] | pause | resume | cancel
review     tui | status | usage | sessions [--drop active] | logs | report [--since 24h] | models | evidence export --out DIR
           reproduce --run ID --out DIR | pr --work-item ID
ship       approval request | approval list | approval approve ID | approval reject ID
           merge --work-item ID | publish --work-item ID | rollback | worktree gc
handoff    takeover --work-item ID | takeover return --work-item ID
evaluate   eval add | eval list | eval spec | eval from-failure --run ID | --approval ID
           eval run [--arm baseline] | eval record | eval compare
operate    backup --out FILE | restore --from FILE --to PATH | effects [--reconcile]
           service systemd [--scope user|system] [--out FILE]
           storage usage | storage prune --older-than 30d [--apply] [--vacuum]
           integrity verify
serve      serve [--addr HOST:PORT] | mcp [--addr HOST:PORT] | storage postgres migrate
           checkpoint --next-action TEXT
misc       version`

// privilegedCommands decide whether work is acceptable or what it is allowed
// to do. An implementation session may change the repository; it may not
// change the gates that judge it, the approvals that release it, or its own
// budget and permissions. Keeping the list in one place is what stops a new
// command becoming reachable from inside a session by omission.
var privilegedCommands = map[string]string{
	"approval approve":         "승인",
	"approval reject":          "승인 반려",
	"approval request":         "승인 요청",
	"goal set":                 "목표 변경",
	"verify gate add":          "검증 게이트 변경",
	"browser gate":             "검증 게이트 변경",
	"pattern approve":          "공통 패턴 권고",
	"pattern retire":           "공통 패턴 철회",
	"verify template":          "검증 게이트 일괄 설정",
	"project budget":           "예산 변경",
	"project profile":          "운영 정책 변경",
	"project runtime":          "실행 제한 변경",
	"project concurrency":      "동시 실행 한도 변경",
	"project provider set":     "제공자·모델 변경",
	"merge":                    "기본 브랜치 병합",
	"publish":                  "원격 게시",
	"eval spec":                "평가 기준 변경",
	"takeover":                 "작업 인계",
	"storage postgres migrate": "저장소 마이그레이션",
	"restore":                  "상태 복원",
}

// commandAuthority names the operation for a command line, or an empty string
// when the command is one an implementation session may run.
func commandAuthority(args []string) (string, string) {
	for _, width := range []int{3, 2, 1} {
		if len(args) < width {
			continue
		}
		key := strings.Join(args[:width], " ")
		if operation, ok := privilegedCommands[key]; ok {
			return key, operation
		}
	}
	return "", ""
}

func run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return usage()
	}
	// Version is answered before anything opens a database or creates a
	// directory: the first thing done with a downloaded binary is asking what
	// it is, and that must work in a directory the user has not chosen yet.
	if args[0] == "version" || args[0] == "--version" || args[0] == "-v" {
		fmt.Print(versionInfo())
		return nil
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
	// The authority check happens before anything is opened or dispatched, so
	// a privileged command cannot take effect part-way before being refused.
	if _, operation := commandAuthority(args); operation != "" {
		if err := policy.RequireOperator(operation); err != nil {
			return err
		}
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
		if len(args) > 1 && args[1] == "profile" {
			return projectProfile(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "sandbox" {
			return projectSandbox(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "relocate" {
			return projectRelocate(ctx, s, args[2:])
		}
	case "service":
		if len(args) > 1 && args[1] == "systemd" {
			return serviceSystemd(ctx, s, args[2:])
		}
	case "tui":
		return runTUI(ctx, s, args[1:])
	case "storage":
		if len(args) > 1 && args[1] == "usage" {
			return storageUsage(ctx, s)
		}
		if len(args) > 1 && args[1] == "prune" {
			return storagePrune(ctx, s, args[2:])
		}
	case "integrity":
		if len(args) > 1 && args[1] == "verify" {
			return integrityVerify(ctx, s, args[2:])
		}
	case "goal":
		if len(args) > 2 && args[1] == "contract" && args[2] == "show" {
			return contractShow(ctx, s)
		}
		if len(args) > 1 && args[1] == "contract" {
			return contractSet(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "set" {
			return goalSet(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "show" {
			return goalShow(ctx, s)
		}
	case "config":
		if len(args) > 1 && args[1] == "calibrate" {
			return configCalibrate(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "selection" {
			return configSelection(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "judge" {
			return configJudge(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "direction" {
			return configDirection(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "propose" {
			return configPropose(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "draft" {
			return configDraft(ctx, s, args[2:])
		}
	case "pattern":
		if len(args) > 1 && args[1] == "list" {
			return patternList(ctx, s)
		}
		if len(args) > 1 && args[1] == "add" {
			return patternAdd(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "apply" {
			return patternApply(ctx, s, args[2:])
		}
		if len(args) > 2 && args[1] == "approve" {
			return patternDecide(ctx, s, args[2], args[3:], false)
		}
		if len(args) > 2 && args[1] == "retire" {
			return patternDecide(ctx, s, args[2], args[3:], true)
		}
	case "capture":
		if len(args) > 1 && args[1] == "status" {
			return captureStatus(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "run" {
			return captureRun(ctx, s, args[2:])
		}
	case "browser":
		if len(args) > 1 && args[1] == "run" {
			return browserRun(ctx, args[2:])
		}
		if len(args) > 1 && args[1] == "check" {
			return browserCheck(ctx, args[2:])
		}
		if len(args) > 1 && args[1] == "gate" {
			return browserGate(ctx, s, args[2:])
		}
	case "standards":
		if len(args) > 1 && args[1] == "profile" {
			return standardsProfile(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "except" {
			return standardsExcept(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "status" {
			return standardsStatus(ctx, s)
		}
		if len(args) > 1 && args[1] == "assess" {
			return standardsAssess(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "pass" {
			return standardsPass(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "gate" {
			return standardsGate(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "settle" {
			return standardsSettle(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "compare" {
			return standardsCompare(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "autonomy" {
			return standardsAutonomy(ctx, s, args[2:])
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
		if len(args) > 2 && args[1] == "template" && args[2] == "list" {
			fmt.Println(strings.Join(store.GateTemplateNames(), "\n"))
			return nil
		}
		if len(args) > 1 && args[1] == "template" {
			return verifyTemplate(ctx, s, args[2:])
		}
	case "plan":
		return planPreview(ctx, s, args[1:])
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
		return sessionsShow(ctx, s, args[1:])
	case "checkpoint":
		return checkpointCreate(ctx, s, args[1:])
	case "logs":
		return logsShow(ctx, s, args[1:])
	case "report":
		return activityReport(ctx, s, args[1:])
	case "models":
		return modelAdvice(ctx, s, args[1:])
	case "pr":
		return prDescription(ctx, s, args[1:])
	case "eval":
		if len(args) > 1 && args[1] == "add" {
			return evalAdd(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "list" {
			return evalList(ctx, s)
		}
		if len(args) > 1 && args[1] == "record" {
			return evalRecord(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "compare" {
			return evalCompare(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "spec" {
			return evalSpec(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "run" {
			return evalRun(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "from-failure" {
			return evalFromFailure(ctx, s, args[2:])
		}
	case "takeover":
		if len(args) > 1 && args[1] == "return" {
			return takeoverReturn(ctx, s, args[2:])
		}
		return takeoverStart(ctx, s, args[1:])
	case "evidence":
		return evidenceExport(ctx, s, args[1:])
	case "backup":
		return backupState(ctx, s, args[1:])
	case "restore":
		return restoreState(ctx, args[1:])
	case "effects":
		return effectsShow(ctx, s, args[1:])
	case "reproduce":
		return reproduceRun(ctx, s, args[1:])
	case "decision":
		if len(args) > 1 && args[1] == "add" {
			return decisionAdd(ctx, s, args[2:])
		}
		if len(args) > 1 && args[1] == "list" {
			return decisionList(ctx, s, args[2:])
		}
		if len(args) > 2 && args[1] == "supersede" {
			return decisionSupersede(ctx, s, args[2:])
		}
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
		if len(args) > 1 && args[1] == "list" {
			return approvalList(ctx, s)
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

// coordination holds the job queue every process in this deployment shares and
// the job store the worker drains. They are resolved together, once, because a
// deployment where the dashboard enqueues into one queue and the worker drains
// another has a button that silently does nothing.
//
// With GOALFORGE_POSTGRES_DSN set, both are the shared PostgreSQL server, which
// is what makes a worker on one machine pick up work requested on another.
// Without it, both are the local SQLite database. Project state itself stays in
// SQLite either way: PostgreSQL coordinates *who runs what*, it is not yet the
// store of record for goals, work items, or evidence.
type coordination struct {
	queue scheduler.Queue
	jobs  scheduler.JobStore
	close func() error
	// Shared is true when the queue is reachable by other machines.
	Shared bool
}

func openCoordination(ctx context.Context, s *store.Store) (coordination, error) {
	dsn := strings.TrimSpace(os.Getenv("GOALFORGE_POSTGRES_DSN"))
	if dsn == "" {
		return coordination{queue: s, jobs: s, close: func() error { return nil }}, nil
	}
	pg, err := pgstore.Open(ctx, dsn)
	if err != nil {
		// Falling back to SQLite here would split the queue in the one
		// configuration that exists to share it, and the symptom would be work
		// that is requested and never runs. Refusing is the safer failure.
		return coordination{}, fmt.Errorf("GOALFORGE_POSTGRES_DSN is set but the server could not be opened: %w", err)
	}
	return coordination{queue: pg, jobs: pg, close: pg.Close, Shared: true}, nil
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
	// Say what the schema is for, and what it is not for. The tables coordinate
	// which machine runs which job; goals, work items, and evidence stay in the
	// local SQLite database.
	fmt.Println("이 스키마는 작업 큐와 프로젝트 점유(lease)를 여러 기계가 공유하기 위한 것입니다.")
	fmt.Println("GOALFORGE_POSTGRES_DSN 을 설정한 프로세스만 이 큐를 사용합니다 — worker, serve, mcp, continue --enqueue 모두 같은 값을 보도록 맞추세요.")
	fmt.Println("목표·작업 항목·검증 증거는 여전히 각 기계의 SQLite 에 있습니다. PostgreSQL 은 '무엇을' 이 아니라 '누가 실행하는지'를 조율합니다.")
	return nil
}

func usage() error {
	return errors.New(usageText)
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
	coord, err := openCoordination(ctx, s)
	if err != nil {
		return err
	}
	defer coord.close()
	handler, err := api.New(s, token, coord.queue)
	if err != nil {
		return err
	}
	if coord.Shared {
		fmt.Println("job queue: PostgreSQL (shared with workers on other machines)")
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
	effect := store.ExternalEffect{ProjectID: project.ID, RunID: commit.RunID, WorkItemID: *workItemID,
		Kind: store.EffectMergeBranch, Target: project.DefaultBranch, Branch: commit.Branch, RequestHash: commit.CommitSHA,
		Key: store.EffectKey(store.EffectMergeBranch, project.ID, *workItemID, project.DefaultBranch, commit.CommitSHA)}
	if guardErr := app.GuardEffect(ctx, s, project, effect); guardErr != nil {
		if errors.Is(guardErr, app.ErrEffectAlreadyApplied) {
			fmt.Printf("already merged: %v\n", guardErr)
			return nil
		}
		return guardErr
	}
	sha, err := gitops.MergeVerified(ctx, project.RepositoryPath, project.DefaultBranch, commit.Branch, message)
	if err != nil {
		if settleErr := s.SettleEffect(ctx, effect.Key, store.EffectUnknown, err.Error()); settleErr != nil {
			return errors.Join(err, settleErr)
		}
		return err
	}
	if err = s.SettleEffect(ctx, effect.Key, store.EffectSucceeded, "merged as "+sha); err != nil {
		return err
	}
	if err = s.MarkIntegrationPending(ctx, project.ID, "병합 후 통합 검증이 필요합니다: "+*workItemID, sha); err != nil {
		return err
	}
	// Evidence gathered inside a worktree says nothing about the branch the
	// change was just merged into, so it stops counting until integration
	// verification runs.
	if _, err = s.InvalidateProjectEvidence(ctx, project.ID, store.StaleCodeChanged); err != nil && !errors.Is(err, store.ErrNotFound) {
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
	// Pushing is an effect outside this database: a crash between doing it and
	// recording it leaves the two disagreeing, so the ledger is consulted
	// first and the remote is asked when a previous attempt is unresolved.
	effect := store.ExternalEffect{ProjectID: project.ID, RunID: commit.RunID, WorkItemID: *workItemID,
		Kind: store.EffectPublishBranch, Target: *remote, Branch: commit.Branch, RequestHash: commit.CommitSHA,
		Key: store.EffectKey(store.EffectPublishBranch, project.ID, *workItemID, *remote, commit.CommitSHA)}
	if guardErr := app.GuardEffect(ctx, s, project, effect); guardErr != nil {
		if errors.Is(guardErr, app.ErrEffectAlreadyApplied) {
			fmt.Printf("already published: %v\n", guardErr)
			return nil
		}
		return guardErr
	}
	if err = gitops.PushBranch(ctx, project.RepositoryPath, *remote, commit.Branch); err != nil {
		// The push may have reached the remote before the failure, so the
		// outcome is unknown rather than failed: retrying a failure is safe,
		// retrying something that may have succeeded is not.
		if settleErr := s.SettleEffect(ctx, effect.Key, store.EffectUnknown, err.Error()); settleErr != nil {
			return errors.Join(err, settleErr)
		}
		return err
	}
	if err = s.SettleEffect(ctx, effect.Key, store.EffectSucceeded, fmt.Sprintf("pushed %s to %s", commit.Branch, *remote)); err != nil {
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
	// Environment checks ask whether this machine can run anything; readiness
	// asks whether this project could ever finish, which is the question a
	// green doctor was quietly failing to answer.
	if projectErr == nil {
		readiness, readinessErr := s.ReadinessInput(ctx, project.ID)
		if readinessErr != nil {
			return readinessErr
		}
		for _, check := range diagnostics.CheckReadiness(readiness) {
			report.Checks = append(report.Checks, check)
			if check.Level == diagnostics.LevelFail {
				report.Failed++
			}
		}
	}
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
	coord, err := openCoordination(ctx, s)
	if err != nil {
		return err
	}
	defer coord.close()
	server, err := mcp.New(s, "", coord.queue)
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
	standardsEvery := f.Duration("standards-every", 15*time.Minute,
		"기준 평가 스윕 주기 (0 이면 워커가 돌지 않습니다). 실제 평가 여부는 프로젝트별 주기·예산이 정합니다")
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
	coord, err := openCoordination(ctx, s)
	if err != nil {
		return err
	}
	defer coord.close()
	owner := fmt.Sprintf("goalforge-worker-%d", os.Getpid())
	worker, err := scheduler.New(coord.jobs, owner, *lease)
	if err != nil {
		return err
	}
	if coord.Shared {
		fmt.Println("job queue: PostgreSQL (shared with other machines)")
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
	// Intents recorded with a state change become work here. Draining before
	// each tick is what makes a crash between the two recoverable: the entry
	// survived, so the follow-up happens on restart.
	drain := func() {
		if published, publishErr := s.PublishOutbox(ctx, ""); publishErr != nil {
			fmt.Fprintln(os.Stderr, "worker outbox error:", publishErr)
		} else if published > 0 {
			fmt.Printf("worker: published %d recorded follow-up(s)\n", published)
		}
	}
	runOnce := func() (bool, error) {
		drain()
		return worker.RunOne(ctx, time.Now().UTC())
	}
	if *once {
		ran, runErr := runOnce()
		fmt.Printf("worker: job_processed=%t\n", ran)
		return runErr
	}
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	lastPrune := time.Time{}
	lastSweep := time.Time{}
	for {
		// The standards sweep rides along with the job pump, the way retention
		// pruning does. Everything the schedule is built from — a daily
		// interval, a discovery budget, an idempotency key that survives two
		// workers seeing the same commit — is machinery for something that
		// runs while nobody is watching, and until something turned it on a
		// timer all of it described a loop that only moved when a person
		// typed a command.
		//
		// This cadence is only how often the question is asked. Whether a
		// project is actually assessed is the project's own interval, budget
		// and backlog floor, which is why asking every fifteen minutes is
		// cheap.
		if now := time.Now().UTC(); *standardsEvery > 0 && now.Sub(lastSweep) >= *standardsEvery {
			sweepStandards(ctx, s)
			lastSweep = now
		}
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

// sweepStandards runs the supply and autonomy loop across every enrolled
// project, reporting only what happened.
//
// A sweep that printed a line per project per tick would bury the one project
// that broke under a hundred saying nothing changed, and an operator who
// scrolls past the log stops reading it.
func sweepStandards(ctx context.Context, s *store.Store) {
	result, err := observer.Tick(ctx, s, version, observer.Default(),
		observer.DefaultSchedulePolicy(), observer.DefaultSupplyPolicy())
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker standards sweep error:", err)
		return
	}
	out, errs := sweepReport(result)
	for _, line := range out {
		fmt.Println(line)
	}
	for _, line := range errs {
		fmt.Fprintln(os.Stderr, line)
	}
}

// sweepReport turns one sweep into the lines an operator reads, keeping
// failures on their own stream so a broken project cannot hide among the quiet
// ones.
func sweepReport(result observer.TickResult) (out, errs []string) {
	if !result.Acted() {
		return nil, nil
	}
	for _, project := range result.Projects {
		switch {
		case project.Err != nil:
			errs = append(errs, fmt.Sprintf("worker standards %s: %v", project.ProjectName, project.Err))
		case project.Ran && len(project.Filed) > 0:
			out = append(out, fmt.Sprintf("worker standards %s: %s — 공급 %d건",
				project.ProjectName, project.Decision.Trigger, len(project.Filed)))
		case project.Ran:
			out = append(out, fmt.Sprintf("worker standards %s: %s — 새 공급 없음", project.ProjectName, project.Decision.Trigger))
		case project.Note != "":
			// Only reached when the sweep already had something to say. A
			// reason nobody prints is a reason nobody has: an operator whose
			// project has been idle for a week could otherwise only find out
			// why by running the command by hand, which is the one thing an
			// unattended loop exists to remove.
			out = append(out, fmt.Sprintf("worker standards %s: %s", project.ProjectName, project.Note))
		}
		if len(project.Approved) > 0 {
			out = append(out, fmt.Sprintf("worker standards %s: 자동 실행 승인 %d건", project.ProjectName, len(project.Approved)))
		}
		if len(project.Merged) > 0 {
			out = append(out, fmt.Sprintf("worker standards %s: 자동 병합 승인 %d건", project.ProjectName, len(project.Merged)))
		}
	}
	return out, errs
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
		case errors.Is(runErr, store.ErrAllCandidatesConflict):
			// Transient: another item is being implemented in the same area.
			// It clears when that run finishes, so wait rather than stop.
			next := time.Now().UTC().Add(30 * time.Second)
			out.RescheduleAt = &next
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
				_ = notify.Post(ctx, notify.Event{Project: project.ID, Name: project.Name, State: "REPAIR_REQUIRED", Reason: plan.Decision + ": " + plan.Reason})
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

// evidenceExport writes the case for what a goal achieved and how it was
// proven. It is assembled from records written as the work happened, including
// the approvals that were refused and the checks that were relaxed: a bundle
// that only keeps the good news describes a different project.
func evidenceExport(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("evidence export", flag.ContinueOnError)
	out := f.String("out", "", "directory to write evidence.html and evidence.json to")
	// Go's flag package stops parsing at the first positional argument, so the
	// "export" verb is taken off before the flags are read rather than
	// silently swallowing everything after it.
	verb, rest := splitLeadingArg(args)
	if verb != "" && verb != "export" {
		return fmt.Errorf("unknown evidence subcommand %q; use: goalforge evidence export --out DIR", verb)
	}
	if err := f.Parse(rest); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	bundle, err := s.BuildEvidenceBundle(ctx, p.ID)
	if err != nil {
		return err
	}
	if *out == "" {
		fmt.Printf("%s — %s\n진행률 %.1f%% · 완료 조건 충족 %t\n작업 %d건 · 승인 %d건 · 설계 결정 %d건 · 기준 완화 %d건\n",
			bundle.Project.Name, bundle.Goal.Title, bundle.Progress.Percent, bundle.Progress.Complete,
			len(bundle.WorkItems), len(bundle.Approvals), len(bundle.Decisions), len(bundle.Relaxations))
		fmt.Println("\n--out DIR 로 evidence.html 과 evidence.json 을 씁니다")
		return nil
	}
	if err = os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(*out, "evidence.json"), encoded, 0o600); err != nil {
		return err
	}
	page, err := os.OpenFile(filepath.Join(*out, "evidence.html"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer page.Close()
	if err = report.EvidenceHTML(page, bundle); err != nil {
		return err
	}
	fmt.Printf("evidence written: %s\n  evidence.html  evidence.json\n", *out)
	return nil
}

// planPreview shows what the next run would do without doing any of it.
// Spending a model call to discover that the budget is exhausted or a gate is
// missing is the expensive way to learn it.
func planPreview(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("plan", flag.ContinueOnError)
	asJSON := f.Bool("json", false, "emit JSON instead of text")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	service, cleanup, err := runtimeService(ctx, s, p)
	if err != nil {
		return err
	}
	defer cleanup()
	plan, err := service.Plan(ctx, p)
	if err != nil {
		return err
	}
	if *asJSON {
		encoded, encodeErr := json.MarshalIndent(plan, "", "  ")
		if encodeErr != nil {
			return encodeErr
		}
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("%s — %s\n", plan.ProjectName, plan.GoalTitle)
	if plan.WorkItem != nil {
		fmt.Printf("다음 작업: %s %s\n  %s\n", plan.WorkItem.ID, plan.WorkItem.Title, plan.SelectionReason)
	}
	for _, skipped := range plan.Skipped {
		fmt.Printf("  건너뜀: %s %s — %s\n", skipped.WorkItemID, skipped.Title, skipped.Reason)
	}
	fmt.Println()
	for _, check := range plan.Checks {
		fmt.Printf("%-5s %-22s %s\n", check.Level, check.Name, check.Detail)
	}
	fmt.Println()
	if plan.Runnable() {
		fmt.Println("이 상태로 `goalforge continue` 를 실행하면 위 작업이 수행됩니다.")
		return nil
	}
	return errors.New("지금은 실행할 수 없습니다. 위의 BLOCK 항목을 먼저 해결하세요")
}

// contractSet records what a goal commits to, as something that can be judged.
// A requirement with no way to settle it is kept and marked unconfirmed rather
// than accepted or dropped: the difference between a requirement and a wish
// has to be visible before work starts, not discovered at completion.
func contractSet(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("goal contract", flag.ContinueOnError)
	title := f.String("title", "", "what the goal is")
	objective := f.String("objective", "", "what done means")
	users := f.String("users", "", "who it is for")
	scenarios := f.String("scenarios", "", "what they do with it")
	outcomes := f.String("outcome", "", "required outcomes: key|method|judge, comma separated (method may itself contain a colon, as in gate:auth_tests)")
	measures := f.String("measure", "", "numeric outcomes: key=metric<op>threshold, e.g. p95=latency_ms<=200")
	exclusions := f.String("exclude", "", "what is deliberately out of scope")
	stage := f.String("stage", "", "completion stage: verified, releasable, deployed, or operating")
	reason := f.String("reason", "", "why the contract changed (required after the first version)")
	decider := f.String("decider", "", "who decided the change")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *title == "" {
		return errors.New("--title is required")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	contract := store.GoalContract{ProjectID: p.ID, Title: *title, Objective: *objective, Users: *users,
		Scenarios: *scenarios, Stage: *stage, ChangeReason: *reason, Decider: *decider,
		Exclusions: splitList(*exclusions)}
	if goal, goalErr := s.CurrentGoal(ctx, p.ID); goalErr == nil {
		contract.GoalID = goal.ID
	} else if !errors.Is(goalErr, store.ErrNotFound) {
		return goalErr
	}
	for _, entry := range splitList(*outcomes) {
		parts := strings.SplitN(entry, "|", 3)
		outcome := store.RequiredOutcome{Key: parts[0], Statement: parts[0]}
		if len(parts) > 1 {
			outcome.Method = parts[1]
		}
		if len(parts) > 2 {
			outcome.Judge = parts[2]
		}
		contract.Outcomes = append(contract.Outcomes, outcome)
	}
	for _, entry := range splitList(*measures) {
		outcome, parseErr := parseMeasuredOutcome(entry)
		if parseErr != nil {
			return parseErr
		}
		contract.Outcomes = append(contract.Outcomes, outcome)
	}
	saved, err := s.SaveContract(ctx, contract)
	if err != nil {
		return err
	}
	fmt.Printf("contract v%d saved: %s (필수 결과 %d건)\n", saved.Version, saved.Title, len(saved.Outcomes))
	reportContract(saved)
	return nil
}

// parseMeasuredOutcome reads key=metric<op>threshold, which is the form that
// makes two requirements comparable enough to contradict each other.
func parseMeasuredOutcome(entry string) (store.RequiredOutcome, error) {
	key, expression, found := strings.Cut(entry, "=")
	if !found {
		return store.RequiredOutcome{}, fmt.Errorf("measured outcome %q must be key=metric<op>threshold", entry)
	}
	for _, operator := range []string{">=", "<=", ">", "<", "="} {
		metric, threshold, ok := strings.Cut(expression, operator)
		if !ok {
			continue
		}
		return store.RequiredOutcome{Key: key, Statement: entry, Metric: strings.TrimSpace(metric),
			Comparator: operator, Threshold: strings.TrimSpace(threshold),
			Method: "gate:" + strings.TrimSpace(metric), Judge: "verification"}, nil
	}
	return store.RequiredOutcome{}, fmt.Errorf("measured outcome %q needs a comparator (>=, <=, >, <, =)", entry)
}

func contractShow(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	contract, err := s.CurrentContract(ctx, p.ID)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Println("no goal contract recorded; `goalforge goal contract --title ... --outcome ...`")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("contract v%d: %s\n", contract.Version, contract.Title)
	if contract.Objective != "" {
		fmt.Printf("  %s\n", contract.Objective)
	}
	if contract.Users != "" || contract.Scenarios != "" {
		fmt.Printf("  대상: %s / 시나리오: %s\n", dashIfEmpty(contract.Users), dashIfEmpty(contract.Scenarios))
	}
	reportContract(contract)
	history, err := s.ContractHistory(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(history) > 1 {
		fmt.Println("\n버전 이력:")
		for _, version := range history {
			fmt.Printf("  v%d  필수 결과 %d건  %s\n", version.Version, len(version.Outcomes), dashIfEmpty(version.ChangeReason))
		}
		fmt.Println("  이전 버전의 필수 결과는 그대로 남습니다. 범위를 줄여도 과거 판정이 바뀌지 않습니다.")
	}
	return nil
}

// reportContract prints what can and cannot yet be judged. Both belong in the
// same place: a contract that only shows its settleable half reads as more
// complete than it is.
func reportContract(contract store.GoalContract) {
	fmt.Println("필수 결과:")
	for _, outcome := range contract.Outcomes {
		mark := "[v]"
		detail := outcome.Method + " / " + outcome.Judge
		if !outcome.Confirmed() {
			mark = "[?]"
			detail = "판정 방법 또는 판정 주체가 없어 미확정"
		}
		fmt.Printf("  %-4s %-16s %s\n", mark, outcome.Key, detail)
	}
	if unconfirmed := contract.Unconfirmed(); len(unconfirmed) > 0 {
		fmt.Printf("\n미확정 %d건: 판정 방법과 주체를 정하기 전까지 이 목표는 완료로 판정될 수 없습니다.\n", len(unconfirmed))
	}
	if conflicts := contract.Conflicts(); len(conflicts) > 0 {
		fmt.Printf("\n상충 %d건:\n", len(conflicts))
		for _, conflict := range conflicts {
			fmt.Printf("  %s vs %s — %s\n", conflict.Left.Key, conflict.Right.Key, conflict.Detail)
		}
		fmt.Println("  어느 쪽도 임의로 없애지 않았습니다. 어떤 것을 바꿀지는 사람이 결정합니다.")
	}
	if len(contract.Exclusions) > 0 {
		fmt.Printf("\n제외 범위: %s\n", strings.Join(contract.Exclusions, ", "))
	}
}

// verifyTemplate installs a starting set of gates for a kind of project. A
// project with no gates cannot complete a goal, and choosing gates from nothing
// is where most setups stall.
func verifyTemplate(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("verify template", flag.ContinueOnError)
	overwrite := f.Bool("overwrite", false, "replace gates of the same type instead of keeping them")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 1 {
		return fmt.Errorf("verify template NAME (available: %s)", strings.Join(store.GateTemplateNames(), ", "))
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	added, skipped, err := s.ApplyGateTemplate(ctx, p.ID, f.Arg(0), *overwrite)
	if err != nil {
		return err
	}
	fmt.Printf("gates added: %s\n", strings.Join(added, ", "))
	if len(skipped) > 0 {
		fmt.Printf("kept existing: %s (use --overwrite to replace)\n", strings.Join(skipped, ", "))
	}
	fmt.Println("review the thresholds before relying on them: a template is a starting point, not a standard")
	// Templates can only install checks that any project of this kind can run.
	// Whether the user's actual task works is project-specific, so saying so
	// here is the difference between a starting point and a false sense of
	// coverage.
	fmt.Println("이 게이트들은 build/test 종류입니다. 사용자 작업이 실제로 끝나는지 확인하려면 verify gate add --kind journey 로 여정 게이트를 추가하고 goal set --criterion 이름@journey=true 로 그 종류를 요구하세요")
	return nil
}

// projectSandbox sets how far a verification command may reach. Gates run code
// the session just wrote, so they are not more trusted than the session: the
// default is the host only because a sandbox that cannot run the project's
// toolchain is worse than none, and only the project knows which image can.
func projectSandbox(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("project sandbox", flag.ContinueOnError)
	mode := f.String("mode", "", "none or docker")
	image := f.String("image", "", "container image that can run this project's gates")
	memory := f.Int("memory-mb", 2048, "memory ceiling")
	cpus := f.Float64("cpus", 2, "CPU ceiling")
	processes := f.Int("processes", 256, "process ceiling")
	network := f.Bool("network", false, "allow the gates to reach the network")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if *mode == "" {
		current, policyErr := s.SandboxPolicy(ctx, p.ID)
		if policyErr != nil {
			return policyErr
		}
		fmt.Printf("sandbox: mode=%s image=%s memory=%dMB cpus=%.1f processes=%d network=%t\n",
			current.Mode, orNone(current.Image), current.MemoryMB, current.CPUs, current.Processes, current.Network)
		if current.Mode == policy.SandboxNone {
			fmt.Println("  검증 명령이 호스트에서 실행됩니다. docker 모드는 작업 공간만 마운트하고 네트워크를 끊습니다:")
			fmt.Println("  goalforge project sandbox --mode docker --image golang:1.23")
		}
		return nil
	}
	sandbox := policy.SandboxPolicy{Mode: *mode, Image: *image, MemoryMB: *memory, CPUs: *cpus, Processes: *processes, Network: *network}
	if err = s.SetSandboxPolicy(ctx, p.ID, sandbox); err != nil {
		return err
	}
	fmt.Printf("sandbox set: mode=%s image=%s memory=%dMB cpus=%.1f processes=%d network=%t\n",
		sandbox.Mode, orNone(sandbox.Image), sandbox.MemoryMB, sandbox.CPUs, sandbox.Processes, sandbox.Network)
	return nil
}

// projectProfile applies an operating posture as a set of limits, so a choice
// like "운영 시스템" is expressed once rather than as a dozen settings.
func projectProfile(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("project profile", flag.ContinueOnError)
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 1 {
		fmt.Printf("project profile NAME (available: %s)\n", strings.Join(store.PolicyProfileNames(), ", "))
		for _, name := range store.PolicyProfileNames() {
			profile, _ := store.LookupPolicyProfile(name)
			fmt.Printf("  %-12s %s\n    토큰 %d, 비용 $%.0f, 하루 %d회, 동시 %d건, 자동 수정 %d회/$%.0f, auto-commit=%t\n",
				profile.Name, profile.Description, profile.TokenLimit, profile.CostLimitUSD, profile.DailyRunLimit,
				profile.WIPLimit, profile.Repair.MaxAttempts, profile.Repair.MaxCostUSD, profile.AutoCommit)
		}
		return nil
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	profile, err := s.ApplyPolicyProfile(ctx, p.ID, f.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("profile applied: %s — %s\n", profile.Name, profile.Description)
	return nil
}

// prDescription emits a pull request body that carries what the change was for
// and how it was proven, so a reviewer does not have to reconstruct it from
// commits.
func prDescription(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("pr", flag.ContinueOnError)
	workItemID := f.String("work-item", "", "work item whose verified change to describe")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *workItemID == "" {
		return errors.New("--work-item is required")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	pr, err := s.BuildPRDescription(ctx, p.ID, *workItemID)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n\n%s", pr.Title, pr.Body)
	return nil
}

// evalAdd registers a fixed task used to compare configurations. Comparing
// before and after on whatever work happened to come up measures the work, not
// the change.
func evalAdd(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("eval add", flag.ContinueOnError)
	name := f.String("name", "", "case name")
	kind := f.String("kind", "", "bug_fix, feature, refactor, or docs")
	title := f.String("goal", "", "goal title the case works toward")
	objective := f.String("objective", "", "what the case asks for")
	notes := f.String("notes", "", "how to judge the result")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	evaluation, err := s.AddEvaluationCase(ctx, store.EvaluationCase{ProjectID: p.ID, Name: *name, Kind: *kind,
		Repository: p.RepositoryPath, GoalTitle: *title, GoalObjective: *objective, Notes: *notes})
	if err != nil {
		return err
	}
	fmt.Printf("evaluation case added: %s %s (%s)\n", evaluation.ID, evaluation.Name, evaluation.Kind)
	return nil
}

func evalList(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	cases, err := s.ListEvaluationCases(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		fmt.Println("no evaluation cases; add one with `goalforge eval add --name ... --kind bug_fix`")
		return nil
	}
	for _, evaluation := range cases {
		fmt.Printf("%-22s %-10s %s\n  %s\n", evaluation.ID, evaluation.Kind, evaluation.Name, evaluation.GoalObjective)
	}
	return nil
}

func evalRecord(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("eval record", flag.ContinueOnError)
	caseID := f.String("case", "", "evaluation case ID")
	label := f.String("label", "", "configuration label being measured")
	runID := f.String("run", "", "run that executed the case")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *caseID == "" || *label == "" || *runID == "" {
		return errors.New("--case, --label, and --run are required")
	}
	if _, err := currentProject(ctx, s); err != nil {
		return err
	}
	result, err := s.RecordEvaluationResult(ctx, *caseID, *label, *runID)
	if err != nil {
		return err
	}
	fmt.Printf("recorded: %s passed=%t tokens=%d cost=$%.4f interventions=%d config=%s\n",
		result.ID, result.Passed, result.Tokens, result.CostUSD, result.Interventions, result.ConfigVersion)
	return nil
}

// evalSpec pins what a case starts from and what judges it, which is what
// turns a record of past runs into something re-executable.
func evalSpec(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("eval spec", flag.ContinueOnError)
	caseID := f.String("case", "", "evaluation case ID")
	fixture := f.String("fixture", "", "repository the task starts from")
	ref := f.String("ref", "", "commit or branch pinning the fixture")
	criteria := f.String("criterion", "", "comma-separated type=value completion criteria, or type@kind=value to demand a kind of proof")
	gateType := f.String("gate-type", "", "criterion the gate measures")
	gateCommand := f.String("gate-command-json", "", "JSON command array for the gate")
	gateValue := f.String("gate-success-value", "true", "value the gate must reach")
	gateKind := f.String("gate-kind", "", "what the gate establishes: "+strings.Join(policy.KnownGateKinds(), ", "))
	seedTitle := f.String("work", "", "seed the trial's backlog with this work item (repeatable via comma separation)")
	seedScope := f.String("work-scope", "", "declared change scope for the seeded work")
	tokens := f.Int64("token-budget", 0, "token ceiling for one trial")
	cost := f.Float64("cost-budget-usd", 0, "cost ceiling for one trial")
	timeout := f.Int("timeout-seconds", 0, "wall-clock ceiling for one trial")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *caseID == "" || *fixture == "" {
		return errors.New("--case and --fixture are required")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	spec, err := s.CaseSpec(ctx, p.ID, *caseID)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(*fixture)
	if err != nil {
		return err
	}
	spec.Fixture, spec.Ref = absolute, *ref
	spec.TokenBudget, spec.CostBudgetUSD, spec.TimeoutSeconds = *tokens, *cost, *timeout
	for _, pair := range splitList(*criteria) {
		// The same syntax the product uses, so a case can demand that a
		// feature be exercised rather than merely compiled — and so both arms
		// of a comparison are judged by that demand.
		criterion, parseErr := model.ParseCriterion(pair)
		if parseErr != nil {
			return parseErr
		}
		spec.Criteria = append(spec.Criteria, evaluation.Criterion{Type: criterion.Type,
			ExpectedValue: criterion.ExpectedValue, RequiredKind: criterion.RequiredKind})
	}
	for _, title := range splitList(*seedTitle) {
		spec.SeedWork = append(spec.SeedWork, evaluation.SeedWorkItem{Title: title, ChangeScope: *seedScope, Priority: 50})
	}
	if *gateType != "" {
		var command []string
		if err = json.Unmarshal([]byte(*gateCommand), &command); err != nil {
			return fmt.Errorf("decode --gate-command-json: %w", err)
		}
		if err = policy.ValidateCommand(command); err != nil {
			return fmt.Errorf("gate command rejected: %w", err)
		}
		if err = policy.ValidGateKind(*gateKind); err != nil {
			return err
		}
		spec.Gates = append(spec.Gates, evaluation.Gate{Type: *gateType, Command: command, Required: true,
			SuccessValue: *gateValue, Kind: *gateKind})
	}
	// The clean state is recorded from the fixture as it stands now, so a
	// later trial starting from anything else is detectable.
	if spec.Ref != "" {
		if _, err = gitops.HeadCommit(ctx, absolute, spec.Ref); err != nil {
			return fmt.Errorf("pin %s in %s: %w", spec.Ref, absolute, err)
		}
	}
	if err = s.SaveCaseSpec(ctx, *caseID, spec); err != nil {
		return err
	}
	fmt.Printf("case pinned: %s fixture=%s ref=%s criteria=%d gates=%d seed_work=%d\n",
		*caseID, spec.Fixture, orNone(spec.Ref), len(spec.Criteria), len(spec.Gates), len(spec.SeedWork))
	if len(spec.SeedWork) == 0 {
		fmt.Println("  note: 초기 작업이 없으면 이 케이스는 구현뿐 아니라 목표 분해까지 측정합니다 (--work 로 고정 가능)")
	}
	if len(spec.Criteria) == 0 {
		fmt.Println("  warning: 완료 조건이 없으면 시행을 판정할 수 없습니다 (--criterion type=value)")
	}
	return nil
}

func orNone(value string) string {
	if value == "" {
		return "(default branch)"
	}
	return value
}

// evalRun re-executes a case in a clean environment the requested number of
// times. Attaching an existing run measures that run; this measures the
// configuration, which is the only thing a comparison can be about.
func evalRun(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("eval run", flag.ContinueOnError)
	caseID := f.String("case", "", "evaluation case ID")
	label := f.String("label", "", "configuration label being measured")
	repeat := f.Int("repeat", 1, "repetitions, run in separate clean environments")
	arm := f.String("arm", evaluation.ArmGoalForge, "goalforge, or baseline for the same model and tools given the same task without GoalForge")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *caseID == "" || *label == "" {
		return errors.New("--case and --label are required")
	}
	if *arm != evaluation.ArmGoalForge && *arm != evaluation.ArmBaseline {
		return fmt.Errorf("--arm must be %s or %s", evaluation.ArmGoalForge, evaluation.ArmBaseline)
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	spec, err := s.CaseSpec(ctx, p.ID, *caseID)
	if err != nil {
		return err
	}
	if spec.Fixture == "" {
		return fmt.Errorf("case %s has no fixture; pin one with `goalforge eval spec --case %s --fixture DIR`", *caseID, *caseID)
	}
	runner := evaluation.Runner{Arm: *arm, Executor: app.ServiceExecutor{
		Provider: p.Provider, Model: p.Model,
		NewSession: func(ctx context.Context, env evaluation.Environment, project model.Project) (evaluation.Session, error) {
			return newTrialSession(ctx, env, project)
		},
	}}
	if *arm == evaluation.ArmBaseline {
		// The baseline runs the same provider and model the project is
		// configured with. Anything else would make the comparison a
		// comparison of models.
		providers, cleanup, providerErr := workerProviders(ctx)
		if providerErr != nil {
			return providerErr
		}
		defer cleanup()
		var chosen provider.Provider
		for _, candidate := range providers {
			if candidate.Name() == p.Provider {
				chosen = candidate
				break
			}
		}
		if chosen == nil {
			return fmt.Errorf("no adapter for provider %q", p.Provider)
		}
		runner.Executor = app.BaselineExecutor{Provider: chosen, Model: p.Model}
	}
	trials, err := runner.Run(ctx, spec, *label, *repeat)
	for _, trial := range trials {
		if recordErr := s.RecordTrial(ctx, trial); recordErr != nil {
			return recordErr
		}
		fmt.Printf("%-16s #%d %-16s %6d tokens $%.4f %5.0fs  %s\n",
			trial.Label, trial.Repetition, trial.Status, trial.Tokens, trial.CostUSD, trial.DurationSeconds, trial.Detail)
	}
	if err != nil {
		return err
	}
	fmt.Printf("\n%d trials recorded for %s. Compare with `goalforge eval compare --case %s`\n", len(trials), *caseID, *caseID)
	return nil
}

func evalCompare(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("eval compare", flag.ContinueOnError)
	caseID := f.String("case", "", "restrict to one case")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	// Re-executed trials and attached runs answer different questions, so they
	// are never averaged together: a trial measures the configuration, an
	// attached run measures the run that happened to be attached.
	trials, err := s.CompareTrials(ctx, p.ID, *caseID)
	if err != nil {
		return err
	}
	if len(trials) > 0 {
		fmt.Println("re-executed trials (clean environment per repetition)")
		fmt.Printf("  %-12s %-20s %6s %8s %9s %12s %14s %10s\n", "arm", "label", "trials", "pass", "stability", "cost/success", "interventions", "avg sec")
		for _, summary := range trials {
			fmt.Printf("  %-12s %-20s %6d %7.0f%% %8.0f%% %12.4f %14.1f %10.0f\n",
				summary.Arm, summary.Label, summary.Trials, summary.PassRate, summary.StableCases,
				summary.CostPerSuccessUSD, summary.AverageInterventions, summary.AverageSeconds)
			if summary.Invalid > 0 || summary.Errored > 0 {
				fmt.Printf("  %-33s 측정 불가 %d건, 오류 %d건 (성공률 분모에서 제외)\n", "", summary.Invalid, summary.Errored)
			}
			if summary.MixedConditions {
				fmt.Printf("  %-33s 조건이 섞여 있어 이 평균은 해석할 수 없습니다\n", "")
			}
		}
		fmt.Println()
		if err = printArmComparison(ctx, s, p.ID, *caseID); err != nil {
			return err
		}
	}
	attached, err := s.CompareEvaluations(ctx, p.ID, *caseID)
	if err != nil {
		return err
	}
	if len(attached) > 0 {
		fmt.Println("attached runs (measure the run that was attached, not a re-execution)")
		fmt.Printf("  %-20s %6s %9s %12s %14s %10s\n", "label", "runs", "pass", "cost/run", "interventions", "avg sec")
		for _, summary := range attached {
			fmt.Printf("  %-20s %6d %8.0f%% %12.4f %14.1f %10.0f\n",
				summary.Label, summary.Runs, summary.PassRate, summary.AverageCostUSD, summary.AverageInterventions, summary.AverageSeconds)
		}
		fmt.Println()
	}
	if len(trials) == 0 && len(attached) == 0 {
		fmt.Println("no evaluation results recorded")
		return nil
	}
	rejections, err := s.RejectionStats(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(rejections) > 0 {
		fmt.Println("rejections by reason:")
		for _, stat := range rejections {
			fmt.Printf("  %-26s %d\n", stat.Category, stat.Count)
			for _, example := range stat.Examples {
				fmt.Printf("    - %s\n", example)
			}
		}
	}
	return nil
}

// takeoverStart hands a work item to a person. Stopping the run comes first:
// handing over a workspace a provider session is still writing to produces a
// conflict neither side can explain.
func takeoverStart(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("takeover", flag.ContinueOnError)
	workItemID := f.String("work-item", "", "work item to take over")
	reason := f.String("reason", "", "why a person is taking this over")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *workItemID == "" {
		return errors.New("--work-item is required")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	goal, err := activeGoal(ctx, s)
	if err != nil {
		return err
	}
	// Ask the running session to stop before claiming the workspace.
	if control, controlErr := s.RequestRunControl(ctx, p.ID, "CANCEL"); controlErr == nil {
		return fmt.Errorf("cancel requested for run %s; wait for it to end, then run takeover again", control.RunID)
	} else if !errors.Is(controlErr, store.ErrNoRunningExecution) {
		return controlErr
	}
	workspace := p.RepositoryPath
	if p.WorktreeEnabled {
		workspace = filepath.Join(p.RepositoryPath+".goalforge-worktrees", *workItemID)
	}
	takeover, err := s.TakeOverWorkItem(ctx, p.ID, goal.ID, *workItemID, *reason, workspace)
	if err != nil {
		return err
	}
	fmt.Printf("taken over: %s work=%s\n  workspace: %s\n", takeover.ID, *workItemID, takeover.Workspace)
	fmt.Println("  automation will not claim this item until `goalforge takeover return --work-item " + *workItemID + "` runs")
	return nil
}

// takeoverReturn gives the item back, records what the person changed as the
// new baseline, and re-verifies: a human edit is not exempt from the gates.
func takeoverReturn(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("takeover return", flag.ContinueOnError)
	workItemID := f.String("work-item", "", "work item to hand back")
	summary := f.String("summary", "", "what was changed by hand")
	skipVerify := f.Bool("skip-verify", false, "hand back without running the gates (the item returns to the backlog unverified)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *workItemID == "" {
		return errors.New("--work-item is required")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	goal, err := activeGoal(ctx, s)
	if err != nil {
		return err
	}
	takeover, err := s.ActiveTakeover(ctx, p.ID, *workItemID)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("work item %s is not currently taken over", *workItemID)
	}
	if err != nil {
		return err
	}
	if !*skipVerify {
		gates, gateErr := s.ListGates(ctx, p.ID)
		if gateErr != nil {
			return gateErr
		}
		if len(gates) == 0 {
			return errors.New("no verification gates configured; use --skip-verify to hand back unverified")
		}
		engine, engineErr := verification.New(s, 1024*1024)
		if engineErr != nil {
			return engineErr
		}
		checks := make([]verification.Gate, 0, len(gates))
		for _, g := range gates {
			checks = append(checks, verification.Gate{Type: g.Type, Command: g.Command, Timeout: g.Timeout, Required: g.Required, SuccessValue: g.SuccessValue, ValuePattern: g.ValuePattern, Kind: g.Kind})
		}
		results, passed, checkErr := engine.Check(ctx, takeover.Workspace, checks)
		if checkErr != nil {
			return checkErr
		}
		records := make([]store.VerificationRecord, 0, len(results))
		for _, result := range results {
			actual := "false"
			if result.Status == "PASSED" {
				actual = "true"
			}
			records = append(records, store.VerificationRecord{CheckType: result.Type, Status: result.Status, ActualValue: actual,
				Output: result.Output, ExitCode: result.ExitCode, Duration: result.Duration, Required: result.Required,
				FailureKind: result.FailureKind, RepairMode: result.RepairMode})
			fmt.Printf("%-8s %-18s exit=%d\n", result.Status, result.Type, result.ExitCode)
		}
		if err = s.RecordHumanEvidence(ctx, goal.ID, *workItemID, records); err != nil {
			return err
		}
		if !passed {
			return errors.New("hand-edited changes do not pass the gates; fix them or use --skip-verify to hand back unverified")
		}
	}
	returned, err := s.ReturnWorkItem(ctx, p.ID, goal.ID, *workItemID, *summary)
	if err != nil {
		return err
	}
	fmt.Printf("returned: %s work=%s held for %s\n", returned.ID, *workItemID, returned.ReturnedAt.Sub(returned.TakenAt).Round(time.Second))
	return nil
}

// backupState writes a consistent copy of the state database.
func backupState(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("backup", flag.ContinueOnError)
	out := f.String("out", "", "file to write the backup to")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("--out is required")
	}
	manifest, err := s.Backup(ctx, *out)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(*out+".manifest.json", encoded, 0o600); err != nil {
		return err
	}
	fmt.Printf("backup written: %s (%d bytes, digest %s)\n  프로젝트 %d · 실행 %d · 외부 효과 %d · 승인 %d · 증거 %d\n",
		*out, manifest.SizeBytes, manifest.Digest, manifest.Projects, manifest.Runs, manifest.Effects,
		manifest.Approvals, manifest.Evidence)
	return nil
}

// restoreState brings a backup up in a new location and checks two things
// before anything is allowed to resume: that the records match what was taken,
// and that nothing GoalForge started outside is still unresolved. Resuming
// with an unsettled push or merge is how a restore produces a duplicate.
func restoreState(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("restore", flag.ContinueOnError)
	from := f.String("from", "", "backup file to restore")
	to := f.String("to", "", "path for the restored state database")
	reconcile := f.Bool("reconcile", true, "settle external effects left unresolved by the failure")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" {
		return errors.New("--from and --to are required")
	}
	if _, err := os.Stat(*to); err == nil {
		return fmt.Errorf("%s already exists; restore refuses to overwrite live state", *to)
	}
	source, err := os.ReadFile(*from)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(*to), 0o750); err != nil {
		return err
	}
	if err = os.WriteFile(*to, source, 0o600); err != nil {
		return err
	}
	restored, err := store.Open(*to)
	if err != nil {
		return err
	}
	defer restored.Close()
	inventory, err := restored.Inventory(ctx)
	if err != nil {
		return err
	}
	if manifestBytes, readErr := os.ReadFile(*from + ".manifest.json"); readErr == nil {
		var manifest store.BackupManifest
		if err = json.Unmarshal(manifestBytes, &manifest); err != nil {
			return err
		}
		differences := manifest.Matches(inventory)
		if len(differences) > 0 {
			return fmt.Errorf("restored records do not match the backup: %s", strings.Join(differences, "; "))
		}
		fmt.Printf("restored and verified against the backup manifest (%s 기준)\n", manifest.TakenAt.Format(time.RFC3339))
	} else {
		fmt.Println("no manifest beside the backup; records restored but not compared")
	}
	fmt.Printf("  프로젝트 %d · 실행 %d · 외부 효과 %d · 승인 %d · 증거 %d\n",
		inventory.Projects, inventory.Runs, inventory.Effects, inventory.Approvals, inventory.Evidence)
	if !*reconcile {
		fmt.Println("  --reconcile=false: 미해결 외부 효과를 정산하지 않았습니다. 재개 전에 goalforge effects --reconcile 을 실행하세요.")
		return nil
	}
	projects, err := restored.ListProjects(ctx)
	if err != nil {
		return err
	}
	unresolved := 0
	for _, project := range projects {
		results, reconcileErr := app.ReconcileAll(ctx, restored, project)
		if reconcileErr != nil {
			return reconcileErr
		}
		for _, result := range results {
			verdict := "확인 불가 — 재개 전에 사람이 판단해야 합니다"
			switch {
			case result.Resolved && result.Applied:
				verdict = "이미 반영됨"
			case result.Resolved:
				verdict = "반영되지 않음 — 재시도 가능"
			default:
				unresolved++
			}
			fmt.Printf("  %-16s %-14s %s — %s\n", project.Name, result.Effect.Kind, verdict, result.Detail)
		}
	}
	if unresolved > 0 {
		return fmt.Errorf("%d개의 외부 효과 결과를 확인할 수 없습니다. 정산 전에는 재개하지 마세요", unresolved)
	}
	fmt.Println("safe to resume")
	return nil
}

// effectsShow lists what GoalForge changed outside its own database and
// settles anything whose outcome was never recorded. A retry that skips this
// is how the same push or merge happens twice.
func effectsShow(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("effects", flag.ContinueOnError)
	reconcile := f.Bool("reconcile", false, "ask the remote about anything unresolved and settle it")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if *reconcile {
		results, reconcileErr := app.ReconcileAll(ctx, s, p)
		if reconcileErr != nil {
			return reconcileErr
		}
		if len(results) == 0 {
			fmt.Println("nothing unresolved")
		}
		for _, result := range results {
			verdict := "확인 불가"
			switch {
			case result.Resolved && result.Applied:
				verdict = "이미 반영됨"
			case result.Resolved:
				verdict = "반영되지 않음"
			}
			fmt.Printf("%-16s %-14s %s — %s\n", result.Effect.Kind, verdict, result.Effect.Branch, result.Detail)
		}
		fmt.Println()
	}
	effects, err := s.ListEffects(ctx, p.ID, 50)
	if err != nil {
		return err
	}
	if len(effects) == 0 {
		fmt.Println("no external effects recorded")
		return nil
	}
	fmt.Printf("%-16s %-12s %-10s %-24s %s\n", "kind", "state", "attempts", "target", "detail")
	for _, effect := range effects {
		fmt.Printf("%-16s %-12s %-10d %-24s %s\n", effect.Kind, effect.State, effect.Attempts,
			effect.Target+"/"+effect.Branch, effect.Result)
	}
	return nil
}

// reproduceRun writes everything needed to put a failure back in front of a
// developer under the same conditions: the commit, the workspace, the exact
// gate commands, and what they printed. It deliberately does not try to make
// the model produce the same output again — that is not reproducible, and it
// is not what investigating a failed run requires.
func reproduceRun(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("reproduce", flag.ContinueOnError)
	runID := f.String("run", "", "run to reproduce")
	out := f.String("out", "", "directory to write the package to (default: print a summary)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *runID == "" {
		return errors.New("--run is required")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	pkg, err := s.BuildReproductionPackage(ctx, p.ID, *runID)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("run %s not found in this project", *runID)
	}
	if err != nil {
		return err
	}
	report := diagnostics.Run(ctx, diagnostics.Options{Providers: []string{pkg.Provider}, StrictCLI: false})
	workspace := pkg.Worktree
	if workspace == "" {
		workspace = pkg.Repository
	}
	if *out == "" {
		fmt.Printf("run %s  state=%s provider=%s model=%s\n", pkg.RunID, pkg.State, pkg.Provider, pkg.Model)
		fmt.Printf("workspace: %s\n", workspace)
		if pkg.BaseCommit != "" {
			fmt.Printf("commit: %s (%s)\n", pkg.BaseCommit, pkg.Branch)
		}
		for _, result := range pkg.Results {
			fmt.Printf("gate %-18s %-8s exit=%d\n", result.CheckType, result.Status, result.ExitCode)
		}
		if pkg.Repair.Decision != "" {
			fmt.Printf("repair: %s — %s\n", pkg.Repair.Decision, pkg.Repair.Reason)
		}
		fmt.Println("\npass --out DIR to write a runnable package")
		return nil
	}
	if err = os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	manifest := map[string]any{"run": pkg, "environment": report, "workspace": workspace}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(*out, "reproduction.json"), encoded, 0o600); err != nil {
		return err
	}
	script := reproductionScript(pkg, workspace)
	scriptPath := filepath.Join(*out, "reproduce.sh")
	if err = os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		return err
	}
	var logs strings.Builder
	for _, result := range pkg.Results {
		logs.WriteString("=== " + result.CheckType + " " + result.Status + " (exit " + strconv.Itoa(result.ExitCode) + ")\n")
		logs.WriteString(result.Output + "\n\n")
	}
	if err = os.WriteFile(filepath.Join(*out, "gate-output.log"), []byte(logs.String()), 0o600); err != nil {
		return err
	}
	fmt.Printf("reproduction package written: %s\n  reproduction.json  reproduce.sh  gate-output.log\n", *out)
	return nil
}

// reproductionScript re-runs the same gates in the same workspace at the same
// commit. It checks the commit rather than checking it out, so it cannot
// silently move a developer's working tree.
func reproductionScript(pkg store.ReproductionPackage, workspace string) string {
	var builder strings.Builder
	builder.WriteString("#!/bin/sh\n# GoalForge reproduction for run " + pkg.RunID + "\n")
	builder.WriteString("# Provider " + pkg.Provider + " model " + pkg.Model + " state " + pkg.State + "\n")
	builder.WriteString("set -eu\ncd " + shellQuote(workspace) + "\n")
	if pkg.BaseCommit != "" {
		builder.WriteString("current=$(git rev-parse HEAD)\n")
		builder.WriteString("if [ \"$current\" != " + shellQuote(pkg.BaseCommit) + " ]; then\n")
		builder.WriteString("  echo \"warning: workspace is at $current, the failure was at " + pkg.BaseCommit + "\" >&2\n")
		builder.WriteString("  echo \"run: git checkout " + pkg.BaseCommit + "\" >&2\nfi\n")
	}
	for _, gate := range pkg.Gates {
		builder.WriteString("\necho '--- " + gate.Type + "'\n")
		quoted := make([]string, 0, len(gate.Command))
		for _, part := range gate.Command {
			quoted = append(quoted, shellQuote(part))
		}
		builder.WriteString(strings.Join(quoted, " ") + " || echo \"" + gate.Type + " failed\"\n")
	}
	return builder.String()
}

// shellQuote wraps a value in single quotes so a path or argument containing
// spaces or shell metacharacters cannot change what the script runs.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// decisionAdd records why a structure was chosen and what was ruled out, so
// later sessions inherit the reasoning instead of re-deriving it or quietly
// reversing it.
func decisionAdd(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("decision add", flag.ContinueOnError)
	title := f.String("title", "", "short name for the decision")
	decision := f.String("decision", "", "what was decided")
	context_ := f.String("context", "", "what problem forced the decision")
	alternatives := f.String("alternatives", "", "what was considered and rejected, and why")
	consequences := f.String("consequences", "", "what this commits the project to")
	workItem := f.String("work-item", "", "work item the decision came out of")
	supersedes := f.String("supersedes", "", "decision ID this replaces")
	scope := f.String("scope", "", "files this decision is about, e.g. 'internal/session/**' — when they change, the decision is flagged for review")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *title == "" || *decision == "" {
		return errors.New("--title and --decision are required")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	goalID := ""
	if goal, goalErr := s.CurrentGoal(ctx, p.ID); goalErr == nil {
		goalID = goal.ID
	} else if !errors.Is(goalErr, store.ErrNotFound) {
		return goalErr
	}
	baseCommit, _ := gitops.HeadCommit(ctx, p.RepositoryPath, p.DefaultBranch)
	record, err := s.RecordDecision(ctx, store.DesignDecision{ProjectID: p.ID, GoalID: goalID, WorkItem: *workItem,
		Title: *title, Context: *context_, Decision: *decision, Alternatives: *alternatives,
		Consequences: *consequences, BaseCommit: baseCommit, Scope: *scope})
	if err != nil {
		return err
	}
	if *supersedes != "" {
		if err = s.SupersedeDecision(ctx, p.ID, *supersedes, record.ID); err != nil {
			return fmt.Errorf("record %s but could not supersede %s: %w", record.ID, *supersedes, err)
		}
		fmt.Printf("decision recorded: %s (supersedes %s)\n", record.ID, *supersedes)
		return nil
	}
	fmt.Printf("decision recorded: %s\n", record.ID)
	if *scope == "" {
		// Without a scope every later commit unsettles the decision, which is
		// correct but makes the signal useless. Say so once, here, rather than
		// leaving the user to wonder why everything needs review.
		fmt.Println("  note: --scope 를 지정하지 않으면 이후 어떤 변경이든 이 결정을 '재확인 필요'로 표시합니다")
	}
	return nil
}

func decisionList(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("decision list", flag.ContinueOnError)
	all := f.Bool("all", false, "include superseded decisions")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	decisions, err := s.ListDecisions(ctx, p.ID, *all)
	if err != nil {
		return err
	}
	if len(decisions) == 0 {
		fmt.Println("no design decisions recorded")
		return nil
	}
	// Each decision is judged against the code it was made about, because a
	// list that presents a note about a since-rewritten module the same way as
	// one about untouched code is the reason stale reasoning survives.
	standings := store.DecisionStandings(ctx, p.RepositoryPath, decisions)
	needReview := 0
	for _, standing := range standings {
		decision := standing.Decision
		fmt.Printf("%s  %-10s %s %s\n  %s\n", decision.ID, decision.Status,
			standingMark(standing.Standing), decision.Title, decision.Decision)
		if decision.Scope != "" {
			fmt.Printf("  범위: %s\n", decision.Scope)
		}
		if standing.NeedsReview() {
			needReview++
			fmt.Printf("  %s\n", standing.Detail)
		}
		if decision.Alternatives != "" {
			fmt.Printf("  제외: %s\n", decision.Alternatives)
		}
		if decision.SupersededBy != "" {
			fmt.Printf("  대체됨: %s\n", decision.SupersededBy)
		}
	}
	if needReview > 0 {
		fmt.Printf("\n재확인 필요 %d건 — 실행 세션에는 '확인되지 않음'으로 전달됩니다\n", needReview)
	}
	return nil
}

func decisionSupersede(ctx context.Context, s *store.Store, args []string) error {
	if len(args) != 2 {
		return errors.New("decision supersede requires the old and the new decision ID")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if err = s.SupersedeDecision(ctx, p.ID, args[0], args[1]); err != nil {
		return err
	}
	fmt.Printf("decision %s superseded by %s\n", args[0], args[1])
	return nil
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
		checks = append(checks, verification.Gate{Type: g.Type, Command: g.Command, Timeout: g.Timeout, Required: g.Required, SuccessValue: g.SuccessValue, ValuePattern: g.ValuePattern, Kind: g.Kind})
	}
	branchSHA, err := gitops.HeadCommit(ctx, p.RepositoryPath, p.DefaultBranch)
	if err != nil {
		return err
	}
	// The tree actually measured, not just the branch tip: an integration
	// check run against a dirty tree proves something about that tree.
	treeSnapshot, err := gitops.TreeID(ctx, p.RepositoryPath)
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
	// The integration run is what proves the criteria on the branch that
	// ships, so its results become the current evidence. A completed goal
	// still needs this recorded: otherwise verifying the integrated result
	// after completion leaves no trace of having done it.
	goal, goalErr := s.CurrentGoal(ctx, p.ID)
	if errors.Is(goalErr, store.ErrNotFound) {
		goal, goalErr = s.LatestGoal(ctx, p.ID)
	}
	if goalErr == nil {
		records := make([]store.VerificationRecord, 0, len(results))
		for _, result := range results {
			actual := "false"
			if result.Status == "PASSED" {
				actual = "true"
			}
			for _, g := range gates {
				if g.Type == result.Type && result.Status == "PASSED" && g.SuccessValue != "" {
					actual = g.SuccessValue
				}
			}
			var evaluator string
			for _, g := range gates {
				if g.Type == result.Type {
					evaluator = store.EvaluatorID(g)
				}
			}
			records = append(records, store.VerificationRecord{CheckType: result.Type, Status: result.Status,
				ActualValue: actual, Output: result.Output, ExitCode: result.ExitCode, Duration: result.Duration,
				Required: result.Required, FailureKind: result.FailureKind, RepairMode: result.RepairMode,
				TreeID: store.WorkspaceTreeID(p.RepositoryPath, treeSnapshot), EvaluatorID: evaluator})
		}
		if err = s.RecordIntegrationEvidence(ctx, goal.ID, branchSHA, records); err != nil {
			return err
		}
	} else if !errors.Is(goalErr, store.ErrNotFound) {
		return goalErr
	}
	if !passed {
		// A failing combination is work, not just an error message: without an
		// item nobody is assigned to it and the branch stays unreleasable with
		// no plan to change that.
		if goalErr == nil {
			item, created, repairErr := s.RecordIntegrationFailure(ctx, p.ID, goal.ID, branchSHA, details)
			if repairErr != nil {
				return repairErr
			}
			verb := "기존 통합 수정 작업을 갱신했습니다"
			if created {
				verb = "통합 수정 작업을 만들었습니다"
			}
			fmt.Printf("%s: %s %s\n", verb, item.ID, item.Title)
		}
		return fmt.Errorf("integration verification failed on %s (%s) — 출시 불가", p.DefaultBranch, strings.Join(details, "; "))
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
	fmt.Printf("runs=%d work_verified=%d tokens=%d cost_usd=%.4f\n", report.Runs, report.WorkCompleted, report.Tokens, report.CostUSD)
	effect := report.Effect
	fmt.Printf("rework=%.0f%% (%d verified / %d needing repair) blocked_for_user=%d takeovers=%d approvals=%d median_approval_wait=%s\n\n",
		effect.ReworkRate, effect.VerifiedRuns, effect.RepairRuns, effect.BlockedForUser, effect.Takeovers, effect.ApprovalsNeeded,
		(time.Duration(effect.MedianApprovalWaitSeconds) * time.Second).Round(time.Second))
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
	action := f.String("action", "protected-files", "protected-files, remove-tests, publish-branch, or merge-branch")
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
	case "remove-tests", store.ApprovalRemoveTests:
		actionType = store.ApprovalRemoveTests
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

// approvalList shows what is waiting on a decision, with the change each one
// covers. Without it the only way to find an approval ID was the dashboard or
// the status summary, which is a poor place to work from when approving
// several at once.
func approvalList(ctx context.Context, s *store.Store) error {
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	approvals, err := s.ListPendingApprovals(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(approvals) == 0 {
		fmt.Println("no approvals waiting")
		return nil
	}
	for _, approval := range approvals {
		fmt.Printf("%s  %-22s %s\n", approval.ID, approval.ActionType, approval.Reason)
		if approval.Scope.Scoped() {
			fmt.Printf("    작업 %s · 커밋 %s · 적용 대상 %s · 파일 %d개\n",
				approval.Scope.WorkItemID, shortSHA(approval.Scope.CommitSHA), approval.Scope.TargetRef, approval.Scope.FilesChanged)
		}
		fmt.Printf("    요청 %s\n", approval.RequestedAt.Local().Format("2006-01-02 15:04"))
	}
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

func approvalReject(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("approval reject", flag.ContinueOnError)
	category := f.String("category", "", "why it was turned down: "+strings.Join(store.RejectionCategories, ", "))
	note := f.String("note", "", "what specifically was wrong")
	approvalID, rest := splitLeadingArg(args)
	if err := f.Parse(rest); err != nil {
		return err
	}
	if approvalID == "" && f.NArg() == 1 {
		approvalID = f.Arg(0)
	}
	if approvalID == "" {
		return errors.New("approval reject requires an approval ID")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if err = s.RejectApprovalWithReason(ctx, p.ID, approvalID, *category, *note); err != nil {
		return err
	}
	fmt.Printf("approval rejected: %s\n", approvalID)
	if *category == "" {
		fmt.Println("  tip: --category records why, which is what turns one rejection into a signal about where automation is weak")
	}
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

func sessionsShow(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("sessions", flag.ContinueOnError)
	drop := f.String("drop", "", "forget a session binding the provider no longer has (or 'active' for the current one)")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if *drop != "" {
		return dropSession(ctx, s, p, *drop)
	}
	sessions, err := s.ListSessions(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		fmt.Printf("%s\t%s\t%s\t%s\n", session.Provider, session.Status, session.SessionID, session.LastRunID)
	}
	// A binding GoalForge calls ACTIVE is a claim about the provider's storage,
	// not about GoalForge's — the provider can discard it at any time and only
	// says so when asked to resume. The run recovers from that on its own now;
	// this line is for the case where someone is looking at the list because
	// something already went wrong.
	if len(sessions) > 0 {
		fmt.Println("\nACTIVE 는 GoalForge 가 기억하는 값입니다. 제공자가 이미 버렸을 수 있으며, 그때는 실행이 스스로 새 세션으로 복구합니다.")
		fmt.Println("직접 끊으려면: goalforge sessions --drop active")
	}
	return nil
}

// dropSession forgets a binding by hand. The run recovers on its own, so this
// is for the operator who already knows the session is gone and does not want
// the next run to spend an attempt discovering it.
func dropSession(ctx context.Context, s *store.Store, p model.Project, target string) error {
	sessionID := target
	if target == "active" {
		session, err := s.ActiveSession(ctx, p.ID, p.Provider)
		if errors.Is(err, store.ErrNotFound) {
			fmt.Println("이 프로젝트에 활성 세션이 없습니다")
			return nil
		}
		if err != nil {
			return err
		}
		sessionID = session.SessionID
	}
	if err := s.InvalidateSession(ctx, p.ID, p.Provider, sessionID,
		"운영자가 직접 끊었습니다", 30*24*time.Hour); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("활성 상태인 세션 %s 을(를) 찾을 수 없습니다", sessionID)
		}
		return err
	}
	fmt.Printf("세션 연결을 끊었습니다: %s\n다음 실행은 새 세션으로 시작합니다.\n", sessionID)
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
	gateKind := f.String("kind", "", "what this gate establishes: "+strings.Join(policy.KnownGateKinds(), ", "))
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
	if err = s.UpsertGate(ctx, p.ID, store.GateConfig{Type: *kind, Command: command, Timeout: time.Duration(*timeout) * time.Second, Required: !*optional, SuccessValue: *success, ValuePattern: *valuePattern, Kind: *gateKind}); err != nil {
		return err
	}
	if *valuePattern != "" {
		fmt.Printf("verification gate configured: %s %v measured=%s threshold=%s kind=%s\n", *kind, command, *valuePattern, *success, gateKindLabel(*gateKind))
		return nil
	}
	fmt.Printf("verification gate configured: %s %v kind=%s\n", *kind, command, gateKindLabel(*gateKind))
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
		coord, coordErr := openCoordination(ctx, s)
		if coordErr != nil {
			return coordErr
		}
		defer coord.close()
		job, jobErr := coord.queue.ScheduleRecurringJob(ctx, store.SchedulerJob{ProjectID: p.ID, Type: "CONTINUE", RunAt: time.Now().UTC(), IdempotencyKey: "continue:" + p.ID})
		if jobErr != nil {
			return jobErr
		}
		where := "run `goalforge worker` to process it"
		if coord.Shared {
			where = "queued in PostgreSQL; any worker on this deployment can process it"
		}
		fmt.Printf("continue job scheduled: %s (%s)\n", job.ID, where)
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
	sandbox, err := s.SandboxPolicy(ctx, p.ID)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	// Refused here rather than at the first gate: "docker rejected an option"
	// reads like the project is broken, when it means the sandbox the project
	// asked for cannot be enforced by the engine that is installed.
	if err = sandbox.CheckEngine(ctx); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	service, err := app.New(s, planning, runner, verifier.WithSandbox(sandbox), nil)
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
	// The same rule the board and the API apply. Setting DONE from here used
	// to work, which meant a person could mark work verified without a gate
	// having run — a boundary the API enforced and this surface did not.
	target := strings.ToUpper(*status)
	if _, err := s.ApplyManualTransition(ctx, g.ID, workID, target, 0); err != nil {
		var refusal *store.TransitionRefusal
		if errors.As(err, &refusal) {
			return fmt.Errorf("%s: %s\n허용된 이동: %s", workID, refusal.Reason,
				strings.Join(allowedTargetsFor(ctx, s, g.ID, workID), ", "))
		}
		return err
	}
	fmt.Printf("work item updated: %s %s\n", workID, target)
	return nil
}

// allowedTargetsFor lists where this item may go, so a refusal is followed by
// the answer rather than by a rule the user has to infer.
func allowedTargetsFor(ctx context.Context, s *store.Store, goalID, workID string) []string {
	item, err := s.WorkItemByID(ctx, goalID, workID)
	if err != nil {
		return nil
	}
	return store.AllowedManualTargets(item.Status)
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
	kind := f.String("kind", "", "무엇을 확인한 것인지: build, test, integration, journey, security, performance, review")
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
	if *kind != "" {
		// Recorded with a kind, this can settle a criterion that asks for that
		// kind. Unclassified evidence cannot — not because a person's word is
		// worth less, but because "something passed" does not say what it
		// established, and a criterion asking for a journey cannot be settled
		// by an unnamed check.
		if err = policy.ValidGateKind(*kind); err != nil {
			return err
		}
		record := store.VerificationRecord{CheckType: *check, Status: strings.ToUpper(*status),
			ActualValue: *actual, Output: *output, EvidenceKind: strings.ToLower(*kind), Required: true}
		if err = s.RecordHumanEvidence(ctx, g.ID, "", []store.VerificationRecord{record}); err != nil {
			return err
		}
		fmt.Printf("verification recorded: %s %s (%s)\n", *check, strings.ToUpper(*status), *kind)
		return nil
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

// currentProject resolves the project registered for this directory.
//
// When none matches, "not found" is true and useless: the state database
// usually does hold a project, registered under a path that has since moved —
// a repository relocated, a clone made somewhere else, or, once a PostgreSQL
// queue is shared, the same project checked out at a different path on this
// machine. Saying which paths are registered turns a dead end into the one
// fact the user needs.
func currentProject(ctx context.Context, s *store.Store) (model.Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return model.Project{}, err
	}
	project, err := s.ProjectByPath(ctx, cwd)
	if !errors.Is(err, store.ErrNotFound) {
		return project, err
	}
	return model.Project{}, unregisteredDirectory(ctx, s, cwd)
}

// unregisteredDirectory explains what is registered instead.
func unregisteredDirectory(ctx context.Context, s *store.Store, cwd string) error {
	projects, listErr := s.ListProjects(ctx)
	if listErr != nil || len(projects) == 0 {
		return fmt.Errorf("%s 에 등록된 프로젝트가 없습니다. goalforge project init 으로 등록하세요", cwd)
	}
	var lines []string
	for _, project := range projects {
		marker := ""
		if _, statErr := os.Stat(project.RepositoryPath); os.IsNotExist(statErr) {
			// A registered path that is gone is the likely explanation, so it
			// is marked rather than listed as if it were still usable.
			marker = "  (경로가 존재하지 않습니다)"
		}
		lines = append(lines, fmt.Sprintf("  %s\t%s%s", project.Name, project.RepositoryPath, marker))
	}
	return fmt.Errorf("현재 디렉터리 %s 에 등록된 프로젝트가 없습니다.\n등록된 프로젝트:\n%s\n저장소를 옮겼다면 `goalforge project relocate` 로 경로를 갱신하세요",
		cwd, strings.Join(lines, "\n"))
}

func goalSet(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("goal set", flag.ContinueOnError)
	title := f.String("title", "", "goal title")
	objective := f.String("objective", "", "objective")
	reason := f.String("reason", "", "change reason")
	var raw listFlag
	f.Var(&raw, "criterion", "type=value completion criterion, or type@kind=value to demand a kind of proof ("+strings.Join(policy.KnownGateKinds(), ", ")+")")
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
		criterion, parseErr := model.ParseCriterion(v)
		if parseErr != nil {
			return parseErr
		}
		criteria = append(criteria, criterion)
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
	// Evidence that no longer describes the current tree or gates stops
	// counting here rather than at whichever write last remembered to say so.
	if _, err = app.RefreshEvidence(ctx, s, p); err != nil {
		return err
	}
	detail, err := s.GoalProgressDetail(ctx, g)
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
	baseline := fmt.Sprintf("%.0f/%.0f 가중치", detail.DoneWeight, detail.TotalWeight)
	if detail.DiscardedItems > 0 {
		baseline += fmt.Sprintf(", 폐기 %d건 기준선 제외", detail.DiscardedItems)
	}
	fmt.Printf("Project: %s\nGoal: %s (v%d, %s)\nState: %s\nProgress: %.1f%% (%s)\nCompletion verified: %t\nRuns: total=%d provider_success=%d failed=%d avg_seconds=%.2f\nWork: done=%d blocked=%d\nVerification: passed=%d total=%d rate=%.1f%%\nSessions: %d\nTokens: input=%d output=%d cached=%d reasoning=%d cost_usd=%.4f\n",
		p.Name, g.Title, g.Version, g.Status, p.State, detail.Percent, baseline, detail.Complete,
		metrics.RunsTotal, metrics.RunsSuccessful, metrics.RunsFailed, metrics.AverageRunSeconds,
		metrics.WorkDone, metrics.WorkBlocked, metrics.VerificationPassed, metrics.VerificationTotal,
		verificationRate, metrics.SessionCount, metrics.InputTokens, metrics.OutputTokens,
		metrics.CachedInputTokens, metrics.ReasoningTokens, metrics.CostUSD)
	// Criteria are shown with the evidence that decided them. Listing the
	// thresholds alone left the CLI unable to answer the question the whole
	// completion model exists for: is this met, and by what?
	fmt.Println("Criteria:")
	for _, criterion := range detail.Criteria {
		fmt.Printf("  %-4s %-20s 기준 %-10s 측정 %-10s %s\n", criterionMark(criterion.Status), criterion.Type,
			criterion.ExpectedValue, dashIfEmpty(criterion.ActualValue), criterionEvidence(criterion))
	}
	return printBlockers(ctx, s, p, g)
}

func criterionMark(status string) string {
	switch status {
	case "MET":
		return "[v]"
	case "UNMET":
		return "[!]"
	case "STALE":
		return "[~]"
	case "WRONG_KIND":
		return "[x]"
	default:
		return "[ ]"
	}
}

// gateKindLabel names an unclassified gate rather than printing an empty
// string, so "kind=" never reads as a display bug.
func gateKindLabel(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "unclassified"
	}
	return strings.ToLower(strings.TrimSpace(kind))
}

func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func criterionEvidence(status store.CriterionStatus) string {
	switch status.Status {
	case "STALE":
		return "재검증 필요: " + status.StaleReason
	case "WRONG_KIND":
		return "검증 종류 불일치: " + status.KindMismatch()
	case "NO_EVIDENCE":
		return "증거 없음"
	case "UNMET":
		// The shortfall says which direction the target was and how far the
		// measurement is from it, which is the difference between "rerun it"
		// and "this design cannot get there".
		if status.Shortfall != "" {
			return status.Shortfall
		}
		fallthrough
	default:
		evidence := "근거 " + dashIfEmpty(status.RunID)
		if !status.MeasuredAt.IsZero() {
			evidence += " " + status.MeasuredAt.Local().Format("01-02 15:04")
		}
		return evidence
	}
}

// printBlockers answers "what is stopping this now", which the CLI previously
// left the user to infer from a state code.
func printBlockers(ctx context.Context, s *store.Store, p model.Project, g model.Goal) error {
	var blockers []string
	approvals, err := s.ListPendingApprovals(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, approval := range approvals {
		blockers = append(blockers, fmt.Sprintf("승인 대기: %s — %s (goalforge approval approve %s)", approval.ActionType, approval.Reason, approval.ID))
	}
	integration, err := s.IntegrationStatus(ctx, p.ID)
	if err != nil {
		return err
	}
	if integration.Pending {
		blockers = append(blockers, "통합 검증 필요: "+integration.Reason+" (goalforge verify integration)")
	}
	runs, err := s.ListRecentRuns(ctx, p.ID, 5)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.State != "REPAIR_REQUIRED" && run.State != "FAILED" {
			continue
		}
		plan, planErr := s.RepairPlanForRun(ctx, run.ID)
		if errors.Is(planErr, store.ErrNotFound) {
			blockers = append(blockers, fmt.Sprintf("검증 실패: %s (goalforge reproduce --run %s)", run.ID, run.ID))
			break
		}
		if planErr != nil {
			return planErr
		}
		if !plan.Automatic() {
			blockers = append(blockers, fmt.Sprintf("복구 중단: %s — %s (%s)", run.ID, plan.Reason, plan.Summary))
		}
		break
	}
	items, err := s.ListWorkItems(ctx, g.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Status != "IN_PROGRESS" {
			continue
		}
		if takeover, takeoverErr := s.ActiveTakeover(ctx, p.ID, item.ID); takeoverErr == nil {
			blockers = append(blockers, fmt.Sprintf("사람이 수정 중: %s (%s) — goalforge takeover return --work-item %s", item.ID, takeover.Reason, item.ID))
		} else if !errors.Is(takeoverErr, store.ErrNotFound) {
			return takeoverErr
		}
	}
	if len(blockers) == 0 {
		return nil
	}
	fmt.Println("Needs you:")
	for _, blocker := range blockers {
		fmt.Printf("  - %s\n", blocker)
	}
	return nil
}

// trialSession runs work inside one evaluation trial, against that trial's own
// state database rather than the operator's. Sharing the operator database
// would let one trial see another's runs, and would put evaluation traffic in
// the record of real work.
type trialSession struct {
	db      *store.Store
	service *app.Service
	cleanup func()
}

func newTrialSession(ctx context.Context, env evaluation.Environment, project model.Project) (evaluation.Session, error) {
	db, err := store.Open(env.StateDB)
	if err != nil {
		return nil, err
	}
	service, cleanup, err := runtimeService(ctx, db, project)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &trialSession{db: db, service: service, cleanup: cleanup}, nil
}

func (t *trialSession) Continue(ctx context.Context, project model.Project) (evaluation.Progress, error) {
	result, err := t.service.Continue(ctx, project)
	progress := evaluation.Progress{RunID: result.Run.RunID, Completed: result.Verification.GoalCompleted}
	if usage, usageErr := t.db.RunUsage(ctx, result.Run.RunID); usageErr == nil {
		progress.Tokens = usage.InputTokens + usage.OutputTokens + usage.CachedInputTokens + usage.ReasoningTokens
		progress.CostUSD = usage.CostUSD
	}
	if err != nil {
		progress.Detail = err.Error()
	}
	return progress, err
}

func (t *trialSession) Close() {
	if t.cleanup != nil {
		t.cleanup()
	}
	if t.db != nil {
		t.db.Close()
	}
}

// printArmComparison reports GoalForge against the baseline, or says plainly
// that there is no baseline. A suite that only measures GoalForge cannot
// support a statement about GoalForge being better than not using it, and
// leaving that unsaid is how such statements get made.
func printArmComparison(ctx context.Context, s *store.Store, projectID, caseID string) error {
	comparisons, err := s.CompareArms(ctx, projectID, caseID)
	if err != nil {
		return err
	}
	if len(comparisons) == 0 {
		return nil
	}
	fmt.Println("GoalForge vs 기준선 (같은 모델·도구·예산으로 GoalForge 없이)")
	for _, comparison := range comparisons {
		fmt.Printf("  조건 %s\n", comparison.ConditionHash)
		if !comparison.Comparable {
			fmt.Printf("    비교 불가 — %s\n", comparison.Reason)
			continue
		}
		fmt.Printf("    %-12s 성공률 %5.0f%%  시행 %d  성공당 비용 $%.4f  개입 %.1f\n",
			"goalforge", comparison.GoalForge.PassRate, comparison.GoalForge.Trials,
			comparison.GoalForge.CostPerSuccessUSD, comparison.GoalForge.AverageInterventions)
		fmt.Printf("    %-12s 성공률 %5.0f%%  시행 %d  성공당 비용 $%.4f  개입 %.1f\n",
			"baseline", comparison.Baseline.PassRate, comparison.Baseline.Trials,
			comparison.Baseline.CostPerSuccessUSD, comparison.Baseline.AverageInterventions)
		fmt.Printf("    차이         성공률 %+.0f%%p", comparison.PassRateDelta)
		if comparison.CostPerSuccessRatio > 0 {
			fmt.Printf("  성공당 비용 %.2fx", comparison.CostPerSuccessRatio)
		}
		fmt.Println()
		// Say how thin the evidence is. A difference from three trials is a
		// hint, not a result, and a number printed without its sample size is
		// read as though it had one.
		smallest := comparison.GoalForge.Trials
		if comparison.Baseline.Trials < smallest {
			smallest = comparison.Baseline.Trials
		}
		if smallest < 10 {
			fmt.Printf("    시행이 팔당 %d건뿐입니다 — 방향을 시사할 뿐 수치로 인용할 수 없습니다\n", smallest)
		}
	}
	fmt.Println()
	return nil
}

// integrityVerify checks that the evidence and approvals in the database are the
// ones GoalForge wrote. Everything the tool claims rests on those records, and
// the session it orchestrates has write access to the same file, so "the row
// says PASSED" is only worth something if a row nobody wrote through GoalForge
// can be told apart from one that was.
func integrityVerify(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("integrity verify", flag.ContinueOnError)
	asJSON := f.Bool("json", false, "emit the report as JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	report, err := s.VerifyIntegrity(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		encoded, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		fmt.Println(string(encoded))
		if !report.Intact() {
			return errors.New("integrity check failed")
		}
		return nil
	}
	fmt.Printf("무결성 기록 %d건\n", report.Entries)
	// State which guarantee is actually in force. An unkeyed chain catches an
	// edit; it does not stop whoever made the edit from recomputing the chain.
	if report.Keyed {
		fmt.Println("사슬 보호: 키 있음 — 기록을 고친 사람도 사슬을 다시 계산할 수 없습니다")
	} else {
		fmt.Printf("사슬 보호: 키 없음 — 수정·삭제·무단 삽입은 탐지하지만, 데이터베이스에 쓸 수 있는 사람이 사슬 전체를 다시 계산하는 것은 막지 못합니다 (%s 를 설정하세요)\n", audit.EnvChainKey)
	}
	if report.UnprotectedOutputs > 0 {
		// These verify, and their outputs could still be rewritten without
		// detection. Folding that into "intact" would be the same kind of
		// overstatement the chain exists to catch.
		fmt.Printf("게이트 출력이 보호되지 않는 오래된 증거 %d건 — 이 행들은 검증되지만, 실패 사유를 고쳐도 탐지되지 않습니다\n",
			report.UnprotectedOutputs)
	}
	if report.Intact() {
		fmt.Println("결과: 기록이 GoalForge 가 쓴 그대로입니다")
		return nil
	}
	fmt.Printf("\n발견 %d건:\n", len(report.Findings))
	for _, finding := range report.Findings {
		fmt.Printf("  %-17s %-9s %-10s %s\n", finding.Kind, finding.RecordKind, finding.RecordID, finding.Detail)
	}
	return errors.New("integrity check failed")
}

// runTUI opens the terminal interface. It is a view over the same store every
// other surface uses, and the privileged actions inside it are checked where
// they happen rather than at the door: opening a screen is not a permission.
func runTUI(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("tui", flag.ContinueOnError)
	refresh := f.Duration("refresh", 5*time.Second, "how often to reload state")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *refresh < time.Second {
		return errors.New("--refresh must be at least 1s")
	}
	return tui.Run(ctx, tui.StoreLoader{DB: s}, tui.Options{RefreshEvery: *refresh})
}

// standingMark labels how far a decision can be relied on, in text so the
// listing is readable when piped.
func standingMark(standing string) string {
	switch standing {
	case store.StandingCurrent:
		return "[v]"
	case store.StandingReviewNeeded:
		return "[~]"
	default:
		return "[?]"
	}
}

// evalFromFailure turns something that actually went wrong into a case the
// next configuration can be measured against.
//
// Without it the improvement loop never closes: "we fixed the prompt" is a
// claim about a run that can never be run again, because the repository has
// moved and the only record is a log. Pinning the failure to the commit it
// started from turns it into a question with an answer.
func evalFromFailure(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("eval from-failure", flag.ContinueOnError)
	runID := f.String("run", "", "run that failed")
	approvalID := f.String("approval", "", "approval a person rejected")
	name := f.String("name", "", "case name (default: derived from the work item)")
	kind := f.String("kind", "bug_fix", "bug_fix, feature, refactor, or docs")
	even := f.Bool("even-if-it-passed", false, "build a case from a run that succeeded, as a regression guard")
	if err := f.Parse(args); err != nil {
		return err
	}
	if (*runID == "") == (*approvalID == "") {
		return errors.New("--run 또는 --approval 중 하나가 필요합니다")
	}
	p, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	var failure store.FailureCase
	if *runID != "" {
		failure, err = s.BuildCaseFromRun(ctx, p.ID, *runID, *even)
		if errors.Is(err, store.ErrRunSucceeded) {
			return fmt.Errorf("실행 %s 은(는) 실패하지 않았습니다. 회귀 방지용으로 만들려면 --even-if-it-passed 를 쓰세요", *runID)
		}
	} else {
		failure, err = s.BuildCaseFromApproval(ctx, p.ID, *approvalID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("이 프로젝트에서 해당 실행이나 승인을 찾을 수 없습니다")
	}
	if err != nil {
		return err
	}
	caseName := *name
	if caseName == "" {
		caseName = derivedCaseName(failure)
	}
	notes := failure.Expectation
	if failure.RejectionCategory != "" {
		notes = "반려 사유 분류: " + failure.RejectionCategory + "\n" + notes
	}
	if failure.FailureKind != "" {
		notes = "실패 유형: " + failure.FailureKind + "\n" + notes
	}
	created, err := s.AddEvaluationCase(ctx, store.EvaluationCase{ProjectID: p.ID, Name: caseName, Kind: *kind,
		Repository: failure.Case.Fixture, GoalTitle: failure.Case.GoalTitle,
		GoalObjective: failure.Case.GoalObjective, Notes: notes})
	if err != nil {
		return err
	}
	if err = s.SaveCaseSpec(ctx, created.ID, failure.Case); err != nil {
		return err
	}
	fmt.Printf("evaluation case created from %s: %s (%s)\n", failure.Origin, created.ID, caseName)
	fmt.Printf("  고정: %s @ %s\n", failure.Case.Fixture, shortSHA(failure.Case.Ref))
	fmt.Printf("  완료 조건 %d개, 게이트 %d개, 초기 작업 %d개\n",
		len(failure.Case.Criteria), len(failure.Case.Gates), len(failure.Case.SeedWork))
	if failure.FailureKind != "" {
		fmt.Printf("  실패 유형: %s\n", failure.FailureKind)
	}
	if failure.RejectionCategory != "" {
		fmt.Printf("  반려 사유 분류: %s\n", failure.RejectionCategory)
	}
	fmt.Printf("  %s\n", failure.Expectation)
	fmt.Printf("\n지금 돌려서 현재 구성이 이 실패를 재현하는지 확인하세요:\n")
	fmt.Printf("  goalforge eval run --case %s --label <현재 구성> --repeat 3\n", created.ID)
	fmt.Printf("  goalforge eval run --case %s --label <현재 구성> --repeat 3 --arm baseline\n", created.ID)
	return nil
}

// derivedCaseName names the case after what failed, so a suite reads as a list
// of problems rather than a list of identifiers.
func derivedCaseName(failure store.FailureCase) string {
	base := "실패"
	if len(failure.Case.SeedWork) > 0 && failure.Case.SeedWork[0].Title != "" {
		base = failure.Case.SeedWork[0].Title
	} else if failure.Case.GoalTitle != "" {
		base = failure.Case.GoalTitle
	}
	switch {
	case failure.RejectionCategory != "":
		base += " (반려: " + failure.RejectionCategory + ")"
	case failure.FailureKind != "":
		base += " (" + failure.FailureKind + ")"
	}
	// Case names are unique per project, so a second case from the same work
	// item needs to be distinguishable rather than rejected.
	return fmt.Sprintf("%s %s", base, time.Now().Format("01-02 15:04"))
}

// serviceSystemd emits a unit that runs the worker as a Linux service.
//
// Written by hand, three things go wrong: the binary path, the state database
// path, and the working directory — a worker started from the wrong directory
// finds no registered project and drains an empty queue while looking
// perfectly healthy. Generating it from the running process removes all three,
// and it prints rather than installs, because writing into /etc is the
// operator's decision and not a side effect of asking what the unit should say.
func serviceSystemd(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("service systemd", flag.ContinueOnError)
	scope := f.String("scope", "system", "system or user")
	out := f.String("out", "", "write the unit here instead of printing it")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *scope != "system" && *scope != "user" {
		return errors.New("--scope must be system or user")
	}
	unit, err := app.DefaultServiceUnit(*scope)
	if err != nil {
		return err
	}
	if *scope == "system" {
		if current, userErr := user.Current(); userErr == nil {
			unit.User, unit.Group = current.Username, current.Username
		}
	}
	// The worker must run where a project is registered, so the check happens
	// now rather than at the first start where it looks like a queue problem.
	if _, err = currentProject(ctx, s); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s 에 등록된 프로젝트가 없습니다. 워커는 이 디렉터리에서 실행되므로 먼저 goalforge project init 을 하세요\n\n", unit.WorkingDir)
	}
	rendered, err := unit.Render()
	if err != nil {
		return err
	}
	if *out != "" {
		if err = os.WriteFile(*out, []byte(rendered), 0o644); err != nil {
			return err
		}
		fmt.Printf("unit written: %s\n", *out)
	} else {
		fmt.Print(rendered)
	}
	fmt.Fprintln(os.Stderr)
	for _, note := range unit.InstallNotes() {
		fmt.Fprintln(os.Stderr, note)
	}
	return nil
}

// projectRelocate points a registered project at the directory it is now in.
//
// A project is found by its repository path, so a repository that moves
// becomes unreachable: every command says "no project here" while the goal,
// the evidence, and the approvals are all still in the database. This is the
// whole recovery, and it is deliberately not a re-registration — that would
// start a second project beside the first and leave the history behind.
func projectRelocate(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("project relocate", flag.ContinueOnError)
	name := f.String("name", "", "project to move here (required when more than one is registered)")
	to := f.String("to", "", "new repository path (default: this directory)")
	if err := f.Parse(args); err != nil {
		return err
	}
	target := *to
	if target == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		target = cwd
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		return errors.New("등록된 프로젝트가 없습니다")
	}
	var chosen *model.Project
	switch {
	case *name != "":
		for i := range projects {
			if projects[i].Name == *name {
				chosen = &projects[i]
				break
			}
		}
		if chosen == nil {
			return fmt.Errorf("프로젝트 %q 를 찾을 수 없습니다", *name)
		}
	case len(projects) == 1:
		chosen = &projects[0]
	default:
		// Guessing which project moved would be a coin flip that rewrites the
		// wrong record, so the ambiguity is handed back with the list needed
		// to resolve it.
		var names []string
		for _, project := range projects {
			names = append(names, fmt.Sprintf("  %s\t%s", project.Name, project.RepositoryPath))
		}
		return fmt.Errorf("프로젝트가 여러 개 등록되어 있어 어느 것을 옮길지 알 수 없습니다. --name 으로 지정하세요:\n%s",
			strings.Join(names, "\n"))
	}
	previous := chosen.RepositoryPath
	moved, err := s.RelocateProject(ctx, chosen.ID, target)
	if err != nil {
		return err
	}
	fmt.Printf("project relocated: %s\n  이전: %s\n  현재: %s\n", moved.Name, previous, moved.RepositoryPath)
	// Worktrees were created under the old path and do not follow it, so a
	// silent success here would be followed by a confusing failure later.
	fmt.Println("\n이전 경로에 만들어진 worktree 는 따라오지 않습니다. `goalforge worktree gc` 로 정리하거나 새 경로에서 다시 만들어집니다.")
	return nil
}

// storageUsage says where the database's space went, so pruning is a decision
// rather than a ritual.
func storageUsage(ctx context.Context, s *store.Store) error {
	breakdown, err := s.Storage(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("상태 데이터베이스: %s\n\n", humanBytes(breakdown.Total))
	fmt.Printf("  %-22s %10s %12s\n", "표", "행", "본문")
	for _, row := range breakdown.Rows {
		body := "—"
		if row.Bytes > 0 {
			body = humanBytes(row.Bytes)
		}
		fmt.Printf("  %-22s %10d %12s\n", row.Table, row.Rows, body)
	}
	fmt.Println("\n본문이 큰 쪽이 실제로 자라는 부분입니다. goalforge storage prune --older-than 30d 로 확인하세요.")
	return nil
}

// storagePrune drops the bulk of finished runs. It reports before it removes,
// because audit data deleted on a typo does not come back.
func storagePrune(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("storage prune", flag.ContinueOnError)
	olderThan := f.String("older-than", "30d", "remove bodies from runs that ended before this long ago (30d, 12w, 720h)")
	apply := f.Bool("apply", false, "actually remove; without it this only reports")
	vacuum := f.Bool("vacuum", false, "return the freed space to the filesystem afterwards")
	if err := f.Parse(args); err != nil {
		return err
	}
	window, err := dayDuration(*olderThan)
	if err != nil {
		return err
	}
	if window <= 0 {
		return errors.New("--older-than must be positive")
	}
	report, err := s.Prune(ctx, time.Now().UTC().Add(-window), *apply)
	if err != nil {
		return err
	}
	verb := "제거 대상"
	if report.Applied {
		verb = "제거함"
	}
	fmt.Printf("%s 이전에 끝난 실행 (%s 기준)\n", report.Before.Local().Format("2006-01-02 15:04"), verb)
	fmt.Printf("  제공자 이벤트 본문  %8d건  %s\n", report.EventBodies, humanBytes(report.EventBytes))
	fmt.Printf("  프롬프트 본문       %8d건  %s\n", report.PromptBodies, humanBytes(report.PromptBytes))
	fmt.Printf("  만료된 세션 기록    %8d건\n", report.SessionsDropped)
	if report.Empty() {
		fmt.Println("\n지울 것이 없습니다.")
		return nil
	}
	// What survives matters more than what goes, because it decides whether
	// the operator can still answer the questions they will be asked.
	fmt.Println("\n남는 것: 이벤트와 프롬프트의 기록 자체(시각·종류·해시), 검증 증거, 승인, 무결성 사슬.")
	fmt.Println("사라지는 것: 이벤트 원문과 프롬프트 본문. 무슨 일이 언제 있었는지는 답할 수 있고, 정확히 어떤 문장이었는지는 답할 수 없게 됩니다.")
	if !report.Applied {
		fmt.Printf("\n실제로 지우려면 --apply 를 붙이세요. 지운 감사 자료는 돌아오지 않습니다.\n")
		return nil
	}
	fmt.Printf("\n데이터베이스: %s → %s\n", humanBytes(report.SizeBefore), humanBytes(report.SizeAfter))
	if !*vacuum {
		fmt.Println("빈 공간은 아직 파일 시스템에 반환되지 않았습니다. --vacuum 으로 되돌릴 수 있습니다 (데이터베이스 전체를 다시 씁니다).")
		return nil
	}
	before, after, err := s.Vacuum(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("vacuum: %s → %s\n", humanBytes(before), humanBytes(after))
	return nil
}

// humanBytes keeps sizes readable without implying precision the number does
// not need.
func humanBytes(count int64) string {
	switch {
	case count >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(count)/(1<<30))
	case count >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(count)/(1<<20))
	case count >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(count)/(1<<10))
	default:
		return fmt.Sprintf("%d B", count)
	}
}

// dayDuration parses a retention window, accepting days and weeks.
//
// Go's own parser stops at hours, so "30d" — the unit anyone reaches for when
// talking about how long to keep logs — is a parse error. Writing "720h" in
// the documentation instead would be correct and would make every reader do
// arithmetic to check it.
func dayDuration(value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, errors.New("기간이 필요합니다 (예: 30d, 12w, 720h)")
	}
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if number, found := strings.CutSuffix(trimmed, suffix); found {
			count, err := strconv.ParseFloat(number, 64)
			if err != nil {
				return 0, fmt.Errorf("기간 %q 를 읽을 수 없습니다", value)
			}
			return time.Duration(count * float64(unit)), nil
		}
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("기간 %q 를 읽을 수 없습니다 (예: 30d, 12w, 720h)", value)
	}
	return parsed, nil
}
