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
