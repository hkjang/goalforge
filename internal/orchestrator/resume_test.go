package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/scheduler"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/usage"
)

type fakeInspector struct {
	snapshot gitops.Snapshot
	err      error
}

func (f fakeInspector) Snapshot(context.Context, string) (gitops.Snapshot, error) {
	return f.snapshot, f.err
}

func prepareWaiting(t *testing.T, fake *fakeProvider, snapshot gitops.Snapshot) (context.Context, *store.Store, *Orchestrator, store.SchedulerJob, time.Time) {
	t.Helper()
	ctx, s, project := setup(t)
	project.State = "READY"
	if err := s.TransitionProjectState(ctx, project.ID, "CREATED", "READY"); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRun(ctx, store.RunRecord{ID: "OLD-RUN", ProjectID: project.ID, Provider: "fake", State: "RUNNING"}); err != nil {
		t.Fatal(err)
	}
	sessionEvent := provider.Event{Type: provider.EventSessionStarted, RunID: "OLD-RUN", SessionID: "session-1", Raw: json.RawMessage(`{"type":"session"}`)}
	if err := s.RecordProviderEvent(ctx, project.ID, sessionEvent); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	resume := reset.Add(time.Minute)
	job := store.SchedulerJob{ProjectID: project.ID, Type: "RESUME", RunAt: resume, IdempotencyKey: "quota:" + project.ID, Payload: `{"account_id":"personal","limit_type":"session"}`}
	checkpoint := store.Checkpoint{ProjectID: project.ID, RunID: "OLD-RUN", Provider: "fake", SessionID: "session-1", CommitSHA: snapshot.CommitSHA, Branch: snapshot.Branch, DirtyFiles: snapshot.DirtyFiles, DirtyFingerprint: snapshot.DirtyFingerprint, WorkItemID: "WORK-1", CompletedSummary: "partial", RemainingSteps: "finish", NextAction: "finish implementation"}
	if err := s.EnterQuotaWait(ctx, store.QuotaWindow{Provider: "fake", AccountID: "personal", LimitType: "session", Status: "exhausted", UsedPercent: 100, DetectedAt: now, QuotaResetAt: &reset, ResumeAt: &resume}, checkpoint, job); err != nil {
		t.Fatal(err)
	}
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, s, o, job, resume
}

func TestResumeHandlerRechecksAndResumesSameSession(t *testing.T) {
	snapshot := gitops.Snapshot{CommitSHA: "abc", Branch: "main", DirtyFiles: []string{"work.go"}}
	fake := &fakeProvider{quota: provider.QuotaSnapshot{Provider: "fake", UsedPercent: 20}, events: []provider.Event{{Type: provider.EventCompleted, TurnID: "turn-new", Raw: json.RawMessage(`{"type":"completed"}`)}}}
	ctx, s, o, _, resume := prepareWaiting(t, fake, snapshot)
	defer s.Close()
	sch, _ := scheduler.New(s, "scheduler", time.Minute)
	handler := o.ResumeHandler(ResumeConfig{Owner: "project-worker", LeaseDuration: time.Minute, Policy: usage.DefaultPolicy(), Inspector: fakeInspector{snapshot: snapshot}, Now: func() time.Time { return resume }, NewRunID: func() string { return "NEW-RUN" }})
	if err := sch.Handle("RESUME", handler); err != nil {
		t.Fatal(err)
	}
	ran, err := sch.RunOne(ctx, resume)
	if err != nil || !ran {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	if fake.resumeCalls != 1 || fake.resumeID != "session-1" {
		t.Fatalf("fake=%+v", fake)
	}
	project, err := s.ProjectByID(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if project.State != "VERIFYING" {
		t.Fatalf("state=%s", project.State)
	}
}

func TestWaitingQuotaResumeSurvivesStoreAndWorkerRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	repository := t.TempDir()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	project := model.Project{ID: "PRJ-RESTART", Name: "restart", RepositoryPath: repository, DefaultBranch: "main", Provider: "fake", Model: "test"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionProjectState(ctx, project.ID, "CREATED", "READY"); err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, store.RunRecord{ID: "RUN-BEFORE-RESTART", ProjectID: project.ID, Provider: "fake", State: "RUNNING"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordProviderEvent(ctx, project.ID, provider.Event{Type: provider.EventSessionStarted, RunID: "RUN-BEFORE-RESTART", SessionID: "session-restart", Raw: json.RawMessage(`{"type":"session"}`)}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	resumeAt := now.Add(time.Minute)
	snapshot := gitops.Snapshot{CommitSHA: "abc", Branch: "main", DirtyFiles: []string{"work.go"}, DirtyFingerprint: "content-v1"}
	checkpoint := store.Checkpoint{ProjectID: project.ID, RunID: "RUN-BEFORE-RESTART", Provider: "fake", Model: "test", SessionID: "session-restart", WorkItemID: "WORK-RESTART", CommitSHA: snapshot.CommitSHA, Branch: snapshot.Branch, DirtyFiles: snapshot.DirtyFiles, DirtyFingerprint: snapshot.DirtyFingerprint, RemainingSteps: "finish repair", NextAction: "continue repair"}
	quota := store.QuotaWindow{Provider: "fake", AccountID: "personal", LimitType: "session", Status: "exhausted", UsedPercent: 100, DetectedAt: now, QuotaResetAt: &resumeAt, ResumeAt: &resumeAt}
	job := store.SchedulerJob{ProjectID: project.ID, Type: "RESUME", RunAt: resumeAt, IdempotencyKey: "quota:" + project.ID, Payload: `{"account_id":"personal","limit_type":"session"}`}
	if err = s.EnterQuotaWait(ctx, quota, checkpoint, job); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a new GoalForge process with a new store, provider, orchestrator,
	// and scheduler instance. No in-memory state from the old process is reused.
	s, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fake := &fakeProvider{quota: provider.QuotaSnapshot{Provider: "fake", LimitType: "session", UsedPercent: 10}, events: []provider.Event{{Type: provider.EventCompleted, TurnID: "turn-after-restart", Raw: json.RawMessage(`{"type":"completed"}`)}}}
	runner, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := scheduler.New(s, "worker-after-restart", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler := runner.ResumeHandler(ResumeConfig{Owner: "project-after-restart", LeaseDuration: time.Minute, Policy: usage.DefaultPolicy(), Inspector: fakeInspector{snapshot: snapshot}, Now: func() time.Time { return resumeAt }, NewRunID: func() string { return "RUN-AFTER-RESTART" }})
	if err = worker.Handle("RESUME", handler); err != nil {
		t.Fatal(err)
	}
	ran, err := worker.RunOne(ctx, resumeAt)
	if err != nil || !ran {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	if fake.resumeCalls != 1 || fake.resumeID != "session-restart" {
		t.Fatalf("resumeCalls=%d session=%s", fake.resumeCalls, fake.resumeID)
	}
	reopenedProject, err := s.ProjectByID(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedProject.State != "VERIFYING" {
		t.Fatalf("state=%s", reopenedProject.State)
	}
	if _, err = s.ClaimDueJob(ctx, resumeAt.Add(time.Hour), "duplicate-worker", time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("completed resume job was claimable again: %v", err)
	}
}

func TestResumeHandlerReschedulesWhenQuotaStillLimited(t *testing.T) {
	snapshot := gitops.Snapshot{CommitSHA: "abc", Branch: "main"}
	nextReset := time.Date(2026, 7, 16, 15, 0, 0, 0, time.UTC)
	fake := &fakeProvider{quota: provider.QuotaSnapshot{Provider: "fake", LimitType: "session", UsedPercent: 100, LimitReached: true, ResetAt: &nextReset}}
	ctx, s, o, _, resume := prepareWaiting(t, fake, snapshot)
	defer s.Close()
	sch, _ := scheduler.New(s, "scheduler", time.Minute)
	handler := o.ResumeHandler(ResumeConfig{Owner: "project-worker", LeaseDuration: time.Minute, Policy: usage.DefaultPolicy(), Inspector: fakeInspector{snapshot: snapshot}, Now: func() time.Time { return resume }})
	_ = sch.Handle("RESUME", handler)
	ran, err := sch.RunOne(ctx, resume)
	if !ran || err == nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	if fake.resumeCalls != 0 {
		t.Fatal("provider was called while quota exhausted")
	}
	if _, err = s.ClaimDueJob(ctx, resume.Add(time.Minute), "early", time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("early claim err=%v", err)
	}
	job, err := s.ClaimDueJob(ctx, nextReset.Add(time.Minute), "later", time.Minute)
	if err != nil || job.Attempts != 2 {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}

func TestResumeHandlerDoesNotResumePastDailyRunLimit(t *testing.T) {
	snapshot := gitops.Snapshot{CommitSHA: "abc", Branch: "main"}
	fake := &fakeProvider{quota: provider.QuotaSnapshot{Provider: "fake", UsedPercent: 10}, events: []provider.Event{{Type: provider.EventCompleted, TurnID: "turn", Raw: json.RawMessage(`{"type":"completed"}`)}}}
	ctx, s, o, _, resume := prepareWaiting(t, fake, snapshot)
	defer s.Close()
	if err := s.SetDailyLimits(ctx, "PRJ-1", 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	sch, _ := scheduler.New(s, "scheduler", time.Minute)
	handler := o.ResumeHandler(ResumeConfig{Owner: "project-worker", LeaseDuration: time.Minute, Policy: usage.DefaultPolicy(), Inspector: fakeInspector{snapshot: snapshot}, Now: func() time.Time { return resume }, NewRunID: func() string { return "SHOULD-NOT-RUN" }})
	_ = sch.Handle("RESUME", handler)
	ran, err := sch.RunOne(ctx, resume)
	if !ran || !errors.Is(err, ErrDailyLimitExceeded) {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	if fake.resumeCalls != 0 || fake.startCalls != 0 {
		t.Fatalf("provider called: start=%d resume=%d", fake.startCalls, fake.resumeCalls)
	}
	project, _ := s.ProjectByID(ctx, "PRJ-1")
	if project.State != "BLOCKED" {
		t.Fatalf("state=%s", project.State)
	}
}

func TestResumeHandlerBlocksOnRepositoryChange(t *testing.T) {
	saved := gitops.Snapshot{CommitSHA: "abc", Branch: "main"}
	fake := &fakeProvider{quota: provider.QuotaSnapshot{UsedPercent: 10}}
	ctx, s, o, _, resume := prepareWaiting(t, fake, saved)
	defer s.Close()
	sch, _ := scheduler.New(s, "scheduler", time.Minute)
	_ = sch.Handle("RESUME", o.ResumeHandler(ResumeConfig{Owner: "worker", LeaseDuration: time.Minute, Policy: usage.DefaultPolicy(), Inspector: fakeInspector{snapshot: gitops.Snapshot{CommitSHA: "changed", Branch: "main"}}, Now: func() time.Time { return resume }}))
	_, err := sch.RunOne(ctx, resume)
	if err == nil {
		t.Fatal("expected repository conflict")
	}
	project, err := s.ProjectByID(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if project.State != "BLOCKED" || fake.resumeCalls != 0 {
		t.Fatalf("state=%s resumeCalls=%d", project.State, fake.resumeCalls)
	}
}

func TestResumeHandlerBlocksOnDirtyContentChange(t *testing.T) {
	saved := gitops.Snapshot{CommitSHA: "abc", Branch: "main", DirtyFiles: []string{"work.go"}, DirtyFingerprint: "before"}
	fake := &fakeProvider{quota: provider.QuotaSnapshot{UsedPercent: 10}}
	ctx, s, o, _, resume := prepareWaiting(t, fake, saved)
	defer s.Close()
	sch, _ := scheduler.New(s, "scheduler", time.Minute)
	current := gitops.Snapshot{CommitSHA: "abc", Branch: "main", DirtyFiles: []string{"work.go"}, DirtyFingerprint: "after"}
	_ = sch.Handle("RESUME", o.ResumeHandler(ResumeConfig{Owner: "worker", LeaseDuration: time.Minute, Policy: usage.DefaultPolicy(), Inspector: fakeInspector{snapshot: current}, Now: func() time.Time { return resume }}))
	_, err := sch.RunOne(ctx, resume)
	if err == nil {
		t.Fatal("expected dirty content conflict")
	}
	project, err := s.ProjectByID(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if project.State != "BLOCKED" || fake.resumeCalls != 0 {
		t.Fatalf("state=%s resumeCalls=%d", project.State, fake.resumeCalls)
	}
}
