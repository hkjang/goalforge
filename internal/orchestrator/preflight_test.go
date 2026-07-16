package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

type runtimeQuotaProvider struct {
	reset                  *time.Time
	quotaCalls, startCalls int
}

func (p *runtimeQuotaProvider) Name() string { return "fake" }
func (p *runtimeQuotaProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredStream: true}
}
func (p *runtimeQuotaProvider) Start(context.Context, provider.RunRequest) (<-chan provider.Event, error) {
	p.startCalls++
	return eventChannel([]provider.Event{{Type: provider.EventFailed, Message: "rate limit", Raw: json.RawMessage(`{"type":"failed"}`)}}), nil
}
func (p *runtimeQuotaProvider) Resume(ctx context.Context, _ string, request provider.RunRequest) (<-chan provider.Event, error) {
	return p.Start(ctx, request)
}
func (p *runtimeQuotaProvider) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	p.quotaCalls++
	if p.quotaCalls == 1 {
		return provider.QuotaSnapshot{Provider: "fake", LimitType: "session", UsedPercent: 10}, nil
	}
	return provider.QuotaSnapshot{Provider: "fake", LimitType: "session", UsedPercent: 100, LimitReached: true, ResetAt: p.reset, Source: "stop_failure", Confidence: "high"}, nil
}
func (p *runtimeQuotaProvider) Interrupt(context.Context, string) error { return nil }

func preparePreflight(t *testing.T, quota provider.QuotaSnapshot) (context.Context, *store.Store, model.Project, model.WorkItem, *fakeProvider, *Orchestrator) {
	t.Helper()
	ctx, s, project := setup(t)
	goal, err := s.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature", Priority: 10})
	if err != nil {
		t.Fatal(err)
	}
	work, err = s.ClaimNextWorkItem(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeProvider{quota: quota, events: []provider.Event{{Type: provider.EventCompleted, TurnID: "turn", Raw: json.RawMessage(`{"type":"completed"}`)}}}
	o, err := New(s, fake)
	if err != nil {
		t.Fatal(err)
	}
	if err = o.ConfigureControl(fakeInspector{snapshot: gitops.Snapshot{CommitSHA: "abc", Branch: "main", DirtyFiles: []string{"work.go"}}}, time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	return ctx, s, project, work, fake, o
}

func TestPreflightSchedulesExactQuotaResumeWithoutProviderCall(t *testing.T) {
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	ctx, s, project, work, fake, o := preparePreflight(t, provider.QuotaSnapshot{Provider: "fake", LimitType: "session", UsedPercent: 100, LimitReached: true, ResetAt: &reset, Source: "app_server", Confidence: "high"})
	defer s.Close()
	result, err := o.Run(ctx, Request{RunID: "R1", WorkItemID: work.ID, Prompt: "work", Project: project, WorkspaceWrite: true})
	if !errors.Is(err, ErrWaitingQuota) || result.State != "WAITING_QUOTA" || fake.startCalls != 0 {
		t.Fatalf("result=%+v startCalls=%d err=%v", result, fake.startCalls, err)
	}
	project, err = s.ProjectByID(ctx, project.ID)
	if err != nil || project.State != "WAITING_QUOTA" {
		t.Fatalf("project=%+v err=%v", project, err)
	}
	cp, err := s.LatestCheckpoint(ctx, project.ID)
	if err != nil || cp.WorkItemID != work.ID || cp.NextAction == "" {
		t.Fatalf("checkpoint=%+v err=%v", cp, err)
	}
	jobs, err := s.ListSchedulerJobs(ctx, project.ID, true)
	if err != nil || len(jobs) != 1 || !jobs[0].RunAt.Equal(reset.Add(time.Minute)) {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
}

func TestPreflightUnknownResetBlocksAndReleasesWork(t *testing.T) {
	ctx, s, project, work, fake, o := preparePreflight(t, provider.QuotaSnapshot{Provider: "fake", LimitType: "session", UsedPercent: 100, LimitReached: true, Source: "error", Confidence: "low"})
	defer s.Close()
	result, err := o.Run(ctx, Request{RunID: "R1", WorkItemID: work.ID, Prompt: "work", Project: project})
	if !errors.Is(err, ErrPreflightBlocked) || result.State != "BLOCKED" || fake.startCalls != 0 {
		t.Fatalf("result=%+v startCalls=%d err=%v", result, fake.startCalls, err)
	}
	project, _ = s.ProjectByID(ctx, project.ID)
	items, _ := s.ListWorkItems(ctx, work.GoalID)
	if project.State != "BLOCKED" || len(items) != 1 || items[0].Status != "BACKLOG" {
		t.Fatalf("project=%+v items=%+v", project, items)
	}
}

func TestPreflightWarningRecordsQuotaAndAllowsRun(t *testing.T) {
	ctx, s, project, work, fake, o := preparePreflight(t, provider.QuotaSnapshot{Provider: "fake", LimitType: "session", UsedPercent: 80, Source: "app_server", Confidence: "high"})
	defer s.Close()
	result, err := o.Run(ctx, Request{RunID: "R1", WorkItemID: work.ID, Prompt: "work", Project: project})
	if err != nil || result.State != "VERIFYING" || fake.startCalls != 1 {
		t.Fatalf("result=%+v startCalls=%d err=%v", result, fake.startCalls, err)
	}
	quotas, err := s.ListQuotaWindows(ctx, "fake")
	if err != nil || len(quotas) != 1 || quotas[0].Status != "warning" {
		t.Fatalf("quotas=%+v err=%v", quotas, err)
	}
}

func TestPreflightWarningDefersLargeWorkWithoutProviderCall(t *testing.T) {
	ctx, s, project, work, fake, o := preparePreflight(t, provider.QuotaSnapshot{Provider: "fake", LimitType: "session", UsedPercent: 80, Source: "app_server", Confidence: "high"})
	defer s.Close()
	result, err := o.Run(ctx, Request{RunID: "R-LARGE", WorkItemID: work.ID, Prompt: "large work", Project: project, EstimatedTokens: 20_001})
	if !errors.Is(err, ErrLargeWorkAtQuotaWarning) || result.State != "READY" || fake.startCalls != 0 {
		t.Fatalf("result=%+v startCalls=%d err=%v", result, fake.startCalls, err)
	}
	project, _ = s.ProjectByID(ctx, project.ID)
	items, _ := s.ListWorkItems(ctx, work.GoalID)
	if project.State != "READY" || len(items) != 1 || items[0].Status != "BACKLOG" {
		t.Fatalf("project=%+v items=%+v", project, items)
	}
	quotas, quotaErr := s.ListQuotaWindows(ctx, "fake")
	if quotaErr != nil || len(quotas) != 1 || quotas[0].Status != "warning" {
		t.Fatalf("quotas=%+v err=%v", quotas, quotaErr)
	}
}

func TestPreflightBlocksWhenDailyRunLimitIsExhausted(t *testing.T) {
	ctx, s, project, work, fake, o := preparePreflight(t, provider.QuotaSnapshot{Provider: "fake", LimitType: "session", UsedPercent: 10})
	defer s.Close()
	if err := s.StartRun(ctx, store.RunRecord{ID: "R-EARLIER", ProjectID: project.ID, Provider: "fake"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "R-EARLIER", "FAILED", "READY"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDailyLimits(ctx, project.ID, 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	result, err := o.Run(ctx, Request{RunID: "R-BLOCKED-DAILY", WorkItemID: work.ID, Prompt: "work", Project: project})
	if !errors.Is(err, ErrDailyLimitExceeded) || result.State != "BLOCKED" || fake.startCalls != 0 {
		t.Fatalf("result=%+v startCalls=%d err=%v", result, fake.startCalls, err)
	}
	project, _ = s.ProjectByID(ctx, project.ID)
	items, _ := s.ListWorkItems(ctx, work.GoalID)
	if project.State != "BLOCKED" || len(items) != 1 || items[0].Status != "BACKLOG" {
		t.Fatalf("project=%+v items=%+v", project, items)
	}
}

func TestRuntimeQuotaFailureCheckpointsAndSchedulesResume(t *testing.T) {
	ctx, s, project := setup(t)
	defer s.Close()
	goal, err := s.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature"}); err != nil {
		t.Fatal(err)
	}
	work, err := s.ClaimNextWorkItem(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	fake := &runtimeQuotaProvider{reset: &reset}
	o, _ := New(s, fake)
	_ = o.ConfigureControl(fakeInspector{snapshot: gitops.Snapshot{CommitSHA: "abc", Branch: "main", DirtyFiles: []string{"work.go"}}}, time.Millisecond, 0)
	result, err := o.Run(ctx, Request{RunID: "R1", WorkItemID: work.ID, Prompt: "work", Project: project, WorkspaceWrite: true})
	if !errors.Is(err, ErrWaitingQuota) || result.State != "WAITING_QUOTA" || fake.startCalls != 1 || fake.quotaCalls != 2 {
		t.Fatalf("result=%+v provider=%+v err=%v", result, fake, err)
	}
	checkpoint, cpErr := s.LatestCheckpoint(ctx, project.ID)
	jobs, jobsErr := s.ListSchedulerJobs(ctx, project.ID, true)
	if cpErr != nil || checkpoint.RunID != "R1" || jobsErr != nil || len(jobs) != 1 {
		t.Fatalf("checkpoint=%+v cpErr=%v jobs=%+v jobsErr=%v", checkpoint, cpErr, jobs, jobsErr)
	}
}
