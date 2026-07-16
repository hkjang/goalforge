package sqlite

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
