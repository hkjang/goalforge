package sqlite

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
)

func TestOperationalQueriesAndCancellation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, RunRecord{ID: "R1", ProjectID: p.ID, Provider: p.Provider}); err != nil {
		t.Fatal(err)
	}
	event := provider.Event{Type: provider.EventSessionStarted, RunID: "R1", SessionID: "S1", Usage: &provider.Usage{InputTokens: 10, OutputTokens: 2, CachedInputTokens: 3, CostUSD: .25}, Raw: json.RawMessage(`{"type":"thread.started"}`)}
	if err = s.RecordProviderEvent(ctx, p.ID, event); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.ListSessions(ctx, p.ID)
	if err != nil || len(sessions) != 1 || sessions[0].SessionID != "S1" {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
	events, err := s.ListEventLogs(ctx, p.ID, 10)
	if err != nil || len(events) != 1 || events[0].RunID != "R1" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	metrics, err := s.ProjectMetrics(ctx, p.ID)
	if err != nil || metrics.RunsTotal != 1 || metrics.SessionCount != 1 || metrics.InputTokens != 10 || metrics.CachedInputTokens != 3 || metrics.CostUSD != .25 {
		t.Fatalf("metrics=%+v err=%v", metrics, err)
	}
	reset, resume := time.Now().UTC().Add(time.Hour), time.Now().UTC().Add(time.Hour+time.Minute)
	if err = s.UpsertQuotaWindow(ctx, QuotaWindow{Provider: p.Provider, AccountID: "default", LimitType: "session", Status: "exhausted", UsedPercent: 100, QuotaResetAt: &reset, ResumeAt: &resume, Source: "test", Confidence: "high"}); err != nil {
		t.Fatal(err)
	}
	quotas, err := s.ListQuotaWindows(ctx, p.Provider)
	if err != nil || len(quotas) != 1 || quotas[0].ResumeAt == nil {
		t.Fatalf("quotas=%+v err=%v", quotas, err)
	}
	if _, err = s.ScheduleJob(ctx, SchedulerJob{ProjectID: p.ID, Type: "RESUME", IdempotencyKey: "resume:P1", RunAt: resume}); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ListSchedulerJobs(ctx, p.ID, true)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	count, err := s.CancelProjectJobs(ctx, p.ID)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	jobs, err = s.ListSchedulerJobs(ctx, p.ID, true)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("active jobs=%+v err=%v", jobs, err)
	}
}

func TestListEventLogsValidatesLimit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.ListEventLogs(context.Background(), "P1", 0); err == nil {
		t.Fatal("expected invalid limit error")
	}
}

func TestPromptAndProviderEventAuditRedactsSecrets(t *testing.T) {
	ctx := context.Background()
	t.Setenv("SERVICE_API_KEY", "environment-secret-value")
	t.Setenv("GOALFORGE_AUDIT_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, RunRecord{ID: "R1", ProjectID: p.ID, Provider: p.Provider}); err != nil {
		t.Fatal(err)
	}
	prompt := "use token=environment-secret-value"
	if err = s.RecordPrompt(ctx, "R1", "work_item_execution", prompt); err != nil {
		t.Fatal(err)
	}
	record, err := s.PromptRecord(ctx, "R1")
	if err != nil || record.Template != "work_item_execution" || len(record.EncryptedPrompt) == 0 || record.RedactedPrompt == prompt {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	event := provider.Event{Type: provider.EventMessage, RunID: "R1", Raw: json.RawMessage(`{"message":"environment-secret-value"}`)}
	if err = s.RecordProviderEvent(ctx, p.ID, event); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListEventLogs(ctx, p.ID, 10)
	if err != nil || len(events) != 1 || events[0].Raw == string(event.Raw) {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestApprovalIsExplicitAndSingleUse(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	approval, err := s.RequestApproval(ctx, p.ID, ApprovalProtectedFiles, "rotate test certificate")
	if err != nil {
		t.Fatal(err)
	}
	if used, err := s.ConsumeApproval(ctx, p.ID, ApprovalProtectedFiles, "R1"); err != nil || used {
		t.Fatalf("unapproved request consumed: used=%t err=%v", used, err)
	}
	if err = s.Approve(ctx, p.ID, approval.ID); err != nil {
		t.Fatal(err)
	}
	if used, err := s.ConsumeApproval(ctx, p.ID, ApprovalProtectedFiles, "R1"); err != nil || !used {
		t.Fatalf("approved request not consumed: used=%t err=%v", used, err)
	}
	if used, err := s.ConsumeApproval(ctx, p.ID, ApprovalProtectedFiles, "R2"); err != nil || used {
		t.Fatalf("approval reused: used=%t err=%v", used, err)
	}
}

// An approval is spendable only on the change it was granted for. Previously
// approvals matched on (project, action type) alone, so reviewing one work
// item's commit produced a token any other merge or publish could spend.
func TestScopedApprovalBindsToWorkItemAndCommit(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	reviewed := ApprovalScope{WorkItemID: "W1", SourceBranch: "goalforge/W1", TargetRef: "main", CommitSHA: "aaaaaaaaaaaabbbb", FilesChanged: 3}
	if _, err = s.RequestScopedApproval(ctx, p.ID, ApprovalMergeBranch, "review W1", ApprovalScope{WorkItemID: "W1"}); err == nil {
		t.Fatal("a merge approval without a commit must be refused")
	}
	approval, err := s.RequestScopedApproval(ctx, p.ID, ApprovalMergeBranch, "review W1", reviewed)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Approve(ctx, p.ID, approval.ID); err != nil {
		t.Fatal(err)
	}
	// Another work item cannot spend it.
	other := ApprovalScope{WorkItemID: "W2", SourceBranch: "goalforge/W2", TargetRef: "main", CommitSHA: "ccccccccccccdddd"}
	if used, err := s.ConsumeScopedApproval(ctx, p.ID, ApprovalMergeBranch, "R9", other); err != nil || used {
		t.Fatalf("approval leaked to another work item: used=%t err=%v", used, err)
	}
	// The same work item at a different commit is a change made after review.
	moved := reviewed
	moved.CommitSHA = "eeeeeeeeeeeeffff"
	var stale *StaleApprovalError
	if _, err := s.ConsumeScopedApproval(ctx, p.ID, ApprovalMergeBranch, "R9", moved); !errors.As(err, &stale) {
		t.Fatalf("a commit that moved after approval must be reported as stale: %v", err)
	}
	if stale.Field != "commit" || stale.Approved != reviewed.CommitSHA || stale.Requested != moved.CommitSHA {
		t.Fatalf("stale error lost the comparison: %+v", stale)
	}
	// A destination that was not the one reviewed is not covered either.
	elsewhere := reviewed
	elsewhere.TargetRef = "release"
	if _, err := s.ConsumeScopedApproval(ctx, p.ID, ApprovalMergeBranch, "R9", elsewhere); !errors.As(err, &stale) || stale.Field != "target" {
		t.Fatalf("approval leaked to another target: %v", err)
	}
	// The reviewed change itself goes through, exactly once.
	if used, err := s.ConsumeScopedApproval(ctx, p.ID, ApprovalMergeBranch, "R1", reviewed); err != nil || !used {
		t.Fatalf("reviewed change not approved: used=%t err=%v", used, err)
	}
	if used, err := s.ConsumeScopedApproval(ctx, p.ID, ApprovalMergeBranch, "R2", reviewed); err != nil || used {
		t.Fatalf("approval reused: used=%t err=%v", used, err)
	}
}

// Scoped actions must not fall back to the unscoped path, which would restore
// the hole the scope closes.
func TestUnscopedConsumeRefusesScopedActions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ConsumeApproval(ctx, "P1", ApprovalMergeBranch, "R1"); err == nil {
		t.Fatal("merge approvals must not be consumable without a scope")
	}
}

// Repair is decided by what failed, not by counting retries: a broken
// environment is never retried automatically, and code-fix retries stop at the
// attempt limit instead of spending a budget one attempt at a time.
func TestPlanRepairRoutesByFailureKindAndStopsAtLimits(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	fail := func(runID, kind, mode string) RepairPlan {
		t.Helper()
		if _, execErr := s.db.ExecContext(ctx, `INSERT INTO runs(id,project_id,work_item_id,provider,state,started_at) VALUES(?,?,?,'codex','REPAIR_REQUIRED',?)`,
			runID, p.ID, work.ID, time.Now().UTC().Format(time.RFC3339Nano)); execErr != nil {
			t.Fatal(execErr)
		}
		if _, execErr := s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,run_id,check_type,status,actual_value,required,failure_kind,repair_mode,created_at) VALUES(?,?,'build_passed','FAILED','false',1,?,?,?)`,
			goal.ID, runID, kind, mode, time.Now().UTC().Format(time.RFC3339Nano)); execErr != nil {
			t.Fatal(execErr)
		}
		plan, planErr := s.PlanRepair(ctx, runID, RepairPolicy{MaxAttempts: 2, MaxCostUSD: 5})
		if planErr != nil {
			t.Fatal(planErr)
		}
		return plan
	}
	if plan := fail("R-ENV", "environment", "ENVIRONMENT"); plan.Decision != RepairBlockEnv || plan.Automatic() {
		t.Fatalf("an environment failure must not be retried: %+v", plan)
	}
	if plan := fail("R1", "test_failure", "CODE_FIX"); !plan.Automatic() || plan.Attempt != 1 {
		t.Fatalf("first code-fix attempt: %+v", plan)
	}
	if plan := fail("R2", "build_failure", "CODE_FIX"); !plan.Automatic() || plan.Attempt != 2 {
		t.Fatalf("second code-fix attempt: %+v", plan)
	}
	plan := fail("R3", "test_failure", "CODE_FIX")
	if plan.Decision != RepairBlockAttempts || plan.Automatic() {
		t.Fatalf("the attempt limit must stop the loop: %+v", plan)
	}
	if plan.Summary == "" || plan.Reason == "" {
		t.Fatalf("a blocked repair must explain itself: %+v", plan)
	}
	stored, err := s.RepairPlanForRun(ctx, "R3")
	if err != nil || stored.Decision != RepairBlockAttempts {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

// A run whose required gates all passed has nothing to repair.
func TestPlanRepairWithNoFailedGates(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,project_id,provider,state,started_at) VALUES('R0',?,'codex','FAILED',?)`, p.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanRepair(ctx, "R0", DefaultRepairPolicy())
	if err != nil || plan.Decision != RepairNothingToRepair {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

// Taking over is more than a pause: the run has to have stopped, the workspace
// changes hands, automation stops claiming the item, and what the person did
// comes back as the new baseline.
func TestTakeoverTransfersOwnership(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature", Status: "APPROVED"})
	if err != nil {
		t.Fatal(err)
	}
	// A running session must be stopped before the workspace changes hands.
	if err = s.StartRun(ctx, RunRecord{ID: "R1", ProjectID: p.ID, WorkItemID: work.ID, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TakeOverWorkItem(ctx, p.ID, goal.ID, work.ID, "직접 고친다", "/tmp/ws"); err == nil {
		t.Fatal("taking over while a run executes must be refused")
	}
	if err = s.FinishRun(ctx, "R1", "FAILED", "REPAIR_REQUIRED"); err != nil {
		t.Fatal(err)
	}
	takeover, err := s.TakeOverWorkItem(ctx, p.ID, goal.ID, work.ID, "직접 고친다", "/tmp/ws")
	if err != nil || takeover.Workspace != "/tmp/ws" {
		t.Fatalf("takeover=%+v err=%v", takeover, err)
	}
	// Automation must not pick the item back up while a person holds it. The
	// WIP limit is raised so ownership is the only thing that can refuse it.
	if err = s.SetWIPLimit(ctx, p.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNextWorkItem(ctx, goal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a human-owned item must not be claimed: %v", err)
	}
	if _, err = s.TakeOverWorkItem(ctx, p.ID, goal.ID, work.ID, "again", "/tmp/ws"); err == nil {
		t.Fatal("double takeover must be refused")
	}
	active, err := s.ActiveTakeover(ctx, p.ID, work.ID)
	if err != nil || active.ID != takeover.ID {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	returned, err := s.ReturnWorkItem(ctx, p.ID, goal.ID, work.ID, "인증 흐름 직접 수정")
	if err != nil || returned.ReturnSummary != "인증 흐름 직접 수정" {
		t.Fatalf("returned=%+v err=%v", returned, err)
	}
	if _, err = s.ActiveTakeover(ctx, p.ID, work.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("takeover must be closed: %v", err)
	}
	claimed, err := s.ClaimNextWorkItem(ctx, goal.ID)
	if err != nil || claimed.ID != work.ID {
		t.Fatalf("automation resumes after the item is handed back: %+v err=%v", claimed, err)
	}
}

// AT-04: the outcome is committed and the process dies before the follow-up is
// scheduled. The intent was written in the same transaction as the outcome, so
// a restart turns it into work instead of leaving the goal stopped.
func TestOutboxSurvivesACrashBeforeScheduling(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE projects SET state='CHECKPOINTING' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	// The run finished, the goal is not complete, and the process stops here.
	if err = s.FinalizeCheckpoint(ctx, p.ID, goal.ID, false); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingOutbox(ctx, p.ID)
	if err != nil || len(pending) != 1 || pending[0].Kind != OutboxContinue {
		t.Fatalf("the intent must be recorded with the outcome: %+v err=%v", pending, err)
	}
	jobs, err := s.ListSchedulerJobs(ctx, p.ID, true)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("nothing was scheduled before the crash: %+v err=%v", jobs, err)
	}
	// Restart: the entry becomes work.
	published, err := s.PublishOutbox(ctx, "")
	if err != nil || published != 1 {
		t.Fatalf("published=%d err=%v", published, err)
	}
	jobs, err = s.ListSchedulerJobs(ctx, p.ID, true)
	if err != nil || len(jobs) != 1 || jobs[0].Type != "CONTINUE" {
		t.Fatalf("the follow-up must exist after restart: %+v err=%v", jobs, err)
	}
	// Publishing again — another worker, another restart — produces no second
	// job and no second entry.
	published, err = s.PublishOutbox(ctx, "")
	if err != nil || published != 0 {
		t.Fatalf("a second publish must be a no-op: published=%d err=%v", published, err)
	}
	jobs, err = s.ListSchedulerJobs(ctx, p.ID, true)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("duplicate follow-up scheduled: %+v", jobs)
	}
	if remaining, remainErr := s.PendingOutbox(ctx, p.ID); remainErr != nil || len(remaining) != 0 {
		t.Fatalf("published entries must not stay pending: %+v err=%v", remaining, remainErr)
	}
}

// A completed goal records no intent to continue: the outbox is for work that
// still has somewhere to go.
func TestCompletedGoalRecordsNoFollowUp(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, p.ID, "goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE projects SET state='CHECKPOINTING' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizeCheckpoint(ctx, p.ID, goal.ID, true); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingOutbox(ctx, p.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}
