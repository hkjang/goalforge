package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
)

func TestGoalVersioningAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	g1, err := s.SetGoal(ctx, p.ID, "First", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if g1.Version != 1 {
		t.Fatalf("version=%d", g1.Version)
	}
	if _, err = s.SetGoal(ctx, p.ID, "Second", "objective", "", g1.Criteria); err == nil {
		t.Fatal("expected missing reason error")
	}
	g2, err := s.SetGoal(ctx, p.ID, "Second", "objective", "scope changed", g1.Criteria)
	if err != nil {
		t.Fatal(err)
	}
	if g2.Version != 2 {
		t.Fatalf("version=%d", g2.Version)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.CurrentGoal(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != g2.ID || got.ChangeReason != "scope changed" {
		t.Fatalf("unexpected goal: %+v", got)
	}
}

func TestCompletionRequiresWorkAndVerification(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	g, err := s.SetGoal(ctx, p.ID, "Goal", "objective", "", []model.Criterion{{Type: "test_rate", ExpectedValue: "100"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, done, err := s.GoalProgress(ctx, g); err != nil || done {
		t.Fatalf("empty goal must not complete: done=%v err=%v", done, err)
	}
	if _, err = s.db.Exec(`INSERT INTO work_items(id,goal_id,type,title,status,weight) VALUES('W1',?,'IMPLEMENT','x','DONE',2)`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO verification_results(goal_id,check_type,status,actual_value,created_at) VALUES(?,'test_rate','PASSED','100','now')`, g.ID); err != nil {
		t.Fatal(err)
	}
	progress, done, err := s.GoalProgress(ctx, g)
	if err != nil || !done || progress != 100 {
		t.Fatalf("progress=%v done=%v err=%v", progress, done, err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE goals SET status='COMPLETED' WHERE id=?`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CurrentGoal(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("current goal err=%v", err)
	}
	latest, err := s.LatestGoal(ctx, p.ID)
	if err != nil || latest.ID != g.ID || latest.Status != "COMPLETED" {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

func TestWorkItemDependencyAndWIPLimit(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	g, err := s.SetGoal(ctx, p.ID, "Goal", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: g.ID, Type: "IMPLEMENT", Title: "first", Priority: 10})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W2", GoalID: g.ID, Type: "IMPLEMENT", Title: "second", Priority: 20, Dependency: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkItemStatus(ctx, g.ID, second.ID, "IN_PROGRESS"); err == nil {
		t.Fatal("expected dependency rejection")
	}
	if err := s.SetWorkItemStatus(ctx, g.ID, first.ID, "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	third, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W3", GoalID: g.ID, Type: "IMPLEMENT", Title: "third"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkItemStatus(ctx, g.ID, third.ID, "IN_PROGRESS"); err == nil {
		t.Fatal("expected WIP limit rejection")
	}
	if err := s.SetWorkItemStatus(ctx, g.ID, first.ID, "DONE"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkItemStatus(ctx, g.ID, second.ID, "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListWorkItems(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].ID != second.ID {
		t.Fatalf("active item should be first: %+v", items)
	}
}

func TestMilestonesAndVerification(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	g, err := s.SetGoal(ctx, p.ID, "Goal", "objective", "", []model.Criterion{{Type: "tests", ExpectedValue: "100"}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.CreateMilestone(ctx, model.Milestone{GoalID: g.ID, Title: "Quality", Weight: 2})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListMilestones(ctx, g.ID)
	if err != nil || len(list) != 1 || list[0].ID != m.ID {
		t.Fatalf("milestones=%+v err=%v", list, err)
	}
	if err := s.RecordVerification(ctx, g.ID, "tests", "PASSED", "99", ""); err != nil {
		t.Fatal(err)
	}
	if _, done, err := s.GoalProgress(ctx, g); err != nil || done {
		t.Fatalf("criterion below threshold must fail: %v %v", done, err)
	}
}

func TestProviderEventPersistsSessionAndUsageIdempotently(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, RunRecord{ID: "RUN-1", ProjectID: p.ID, Provider: "codex", Model: "gpt-test"}); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"type":"turn.completed","turn_id":"TURN-1","usage":{"input_tokens":100}}`)
	event := provider.Event{Type: provider.EventCompleted, RunID: "RUN-1", SessionID: "thr_1", TurnID: "TURN-1", Raw: raw, Usage: &provider.Usage{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 40, ReasoningTokens: 5}}
	if err = s.RecordProviderEvent(ctx, p.ID, event); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordProviderEvent(ctx, p.ID, event); err != nil {
		t.Fatal(err)
	}
	session, err := s.ActiveSession(ctx, p.ID, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID != "thr_1" || session.LastRunID != "RUN-1" || session.ContextTokensUsed != 120 {
		t.Fatalf("session=%+v", session)
	}
	usage, err := s.RunUsage(ctx, "RUN-1")
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 20 || usage.CachedInputTokens != 40 || usage.ReasoningTokens != 5 {
		t.Fatalf("usage=%+v", usage)
	}
	var eventCount int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM event_logs WHERE run_id='RUN-1'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("event count=%d", eventCount)
	}
}

func TestProjectBudgetAndQuotaWindow(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = s.SetProjectBudget(ctx, p.ID, 1000, 2.5); err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, RunRecord{ID: "RUN-1", ProjectID: p.ID, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	e := provider.Event{Type: provider.EventCompleted, RunID: "RUN-1", TurnID: "T1", Raw: json.RawMessage(`{"type":"done"}`), Usage: &provider.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: .25}}
	if err = s.RecordProviderEvent(ctx, p.ID, e); err != nil {
		t.Fatal(err)
	}
	b, err := s.ProjectBudgetUsage(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.TokenLimit != 1000 || b.TokensUsed != 120 || b.CostUsedUSD != .25 {
		t.Fatalf("budget=%+v", b)
	}
	if err = s.SetDailyLimits(ctx, p.ID, 5, 500, 1.5); err != nil {
		t.Fatal(err)
	}
	limits, daily, err := s.ProjectDailyUsage(ctx, p.ID, time.Now().UTC())
	if err != nil || limits.DailyRunLimit != 5 || limits.DailyTokenLimit != 500 || limits.DailyCostLimitUSD != 1.5 || daily.Runs != 1 || daily.Tokens != 120 || daily.CostUSD != .25 {
		t.Fatalf("limits=%+v daily=%+v err=%v", limits, daily, err)
	}
	reset := time.Now().UTC().Add(time.Hour)
	resume := reset.Add(time.Minute)
	q := QuotaWindow{Provider: "codex", AccountID: "personal", LimitType: "session", Status: "exhausted", UsedPercent: 100, QuotaResetAt: &reset, ResumeAt: &resume, Source: "app_server", Confidence: "high"}
	if err = s.UpsertQuotaWindow(ctx, q); err != nil {
		t.Fatal(err)
	}
	var gotReset, gotResume string
	if err = s.db.QueryRow(`SELECT quota_reset_at,resume_at FROM quota_windows WHERE provider='codex'`).Scan(&gotReset, &gotResume); err != nil {
		t.Fatal(err)
	}
	if gotReset == gotResume {
		t.Fatal("reset and resume timestamps must remain distinct")
	}
}
