package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
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
	second, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W2", GoalID: g.ID, Type: "IMPLEMENT", Title: "second", Priority: 20, Dependencies: []string{first.ID}})
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

// A discarded work item leaves the goal's baseline instead of sitting in the
// denominator forever; before this it made done == total unreachable, so any
// goal with a discarded item could never complete.
func TestDiscardedWorkLeavesTheGoalBaseline(t *testing.T) {
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
	g, err := s.SetGoal(ctx, p.ID, "Goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO work_items(id,goal_id,type,title,status,weight) VALUES('W1',?,'IMPLEMENT','done work','DONE',3)`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO work_items(id,goal_id,type,title,status,weight) VALUES('W2',?,'IMPLEMENT','dropped idea','DISCARDED',2)`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO verification_results(goal_id,check_type,status,actual_value,created_at) VALUES(?,'build_passed','PASSED','true','now')`, g.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GoalProgressDetail(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Percent != 100 || !detail.Complete {
		t.Fatalf("discarded work must not block completion: percent=%v complete=%v", detail.Percent, detail.Complete)
	}
	if detail.TotalWeight != 3 || detail.DiscardedWeight != 2 || detail.DiscardedItems != 1 {
		t.Fatalf("baseline not restated: %+v", detail)
	}
	if len(detail.Criteria) != 1 || detail.Criteria[0].Status != "MET" {
		t.Fatalf("criteria=%+v", detail.Criteria)
	}
}

// Criteria distinguish a measured shortfall from never having been measured,
// so the UI can say "기준 미달" instead of "증거 없음" and vice versa.
func TestCriterionStatusSeparatesShortfallFromMissingEvidence(t *testing.T) {
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
	g, err := s.SetGoal(ctx, p.ID, "Goal", "objective", "", []model.Criterion{
		{Type: "coverage", ExpectedValue: "85"},
		{Type: "deploy_ready", ExpectedValue: "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO verification_results(goal_id,check_type,status,actual_value,created_at) VALUES(?,'coverage','PASSED','71.4','now')`, g.ID); err != nil {
		t.Fatal(err)
	}
	criteria, err := s.CriteriaStatus(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if criteria[0].Status != "UNMET" || !criteria[0].HasEvidence || criteria[0].ActualValue != "71.4" {
		t.Fatalf("shortfall: %+v", criteria[0])
	}
	if criteria[1].Status != "NO_EVIDENCE" || criteria[1].HasEvidence {
		t.Fatalf("missing evidence: %+v", criteria[1])
	}
}

// Work can depend on several predecessors, and a cycle is refused at the point
// it would be created: a cycle is not a slow plan, it is one that never starts.
func TestMultipleDependenciesAndCycleRejection(t *testing.T) {
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
	g, err := s.SetGoal(ctx, p.ID, "Goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "SCHEMA", GoalID: g.ID, Type: "IMPLEMENT", Title: "schema"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "CLIENT", GoalID: g.ID, Type: "IMPLEMENT", Title: "client"})
	if err != nil {
		t.Fatal(err)
	}
	feature, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "FEATURE", GoalID: g.ID, Type: "IMPLEMENT", Title: "feature",
		Dependencies: []string{schema.ID, client.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// Both predecessors must be DONE, not just one.
	if err = s.SetWorkItemStatus(ctx, g.ID, feature.ID, "IN_PROGRESS"); err == nil {
		t.Fatal("an item with two unfinished dependencies must not start")
	}
	if err = s.SetWorkItemStatus(ctx, g.ID, schema.ID, "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, g.ID, schema.ID, "DONE"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, g.ID, feature.ID, "IN_PROGRESS"); err == nil {
		t.Fatal("one satisfied dependency is not enough")
	}
	if err = s.SetWorkItemStatus(ctx, g.ID, client.ID, "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, g.ID, client.ID, "DONE"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, g.ID, feature.ID, "IN_PROGRESS"); err != nil {
		t.Fatalf("all dependencies done: %v", err)
	}
	// SCHEMA depending on FEATURE would close SCHEMA -> FEATURE -> SCHEMA.
	if _, err = s.UpdateWorkItemPlan(ctx, g.ID, schema.ID, WorkItemPlan{Dependencies: []string{feature.ID}}); err == nil ||
		!strings.Contains(err.Error(), "cycle") {
		t.Fatalf("a dependency cycle must be refused: %v", err)
	}
	stored, err := s.WorkItemByID(ctx, g.ID, feature.ID)
	if err != nil || len(stored.Dependencies) != 2 {
		t.Fatalf("dependencies=%v err=%v", stored.Dependencies, err)
	}
}

// Above a WIP limit of one, only items whose declared scopes are disjoint may
// run together: two sessions editing the same files in separate worktrees
// produce a conflict neither of them verified.
func TestConcurrentWorkRequiresDisjointScopes(t *testing.T) {
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
	g, err := s.SetGoal(ctx, p.ID, "Goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	api, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "API", GoalID: g.ID, Type: "IMPLEMENT", Title: "api", ChangeScope: "internal/api/**"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "STORE", GoalID: g.ID, Type: "IMPLEMENT", Title: "store", ChangeScope: "internal/store/**"})
	if err != nil {
		t.Fatal(err)
	}
	overlapping, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "APIDOC", GoalID: g.ID, Type: "IMPLEMENT", Title: "api docs", ChangeScope: "internal/api/**"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, g.ID, api.ID, "IN_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	// The default limit is one.
	if err = s.SetWorkItemStatus(ctx, g.ID, store.ID, "IN_PROGRESS"); err == nil || !strings.Contains(err.Error(), "WIP limit") {
		t.Fatalf("default WIP limit: %v", err)
	}
	if err = s.SetWIPLimit(ctx, p.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorkItemStatus(ctx, g.ID, overlapping.ID, "IN_PROGRESS"); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlapping scopes must be refused: %v", err)
	}
	if err = s.SetWorkItemStatus(ctx, g.ID, store.ID, "IN_PROGRESS"); err != nil {
		t.Fatalf("disjoint scopes may run together: %v", err)
	}
	if err = s.SetWIPLimit(ctx, p.ID, 0); err == nil {
		t.Fatal("a WIP limit below one must be refused")
	}
}

// Merging leaves the default branch unverified until integration verification
// runs: each item verified in its own worktree, never their combination.
func TestIntegrationVerificationGap(t *testing.T) {
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
	status, err := s.IntegrationStatus(ctx, p.ID)
	if err != nil || status.Pending {
		t.Fatalf("nothing merged yet: %+v err=%v", status, err)
	}
	if err = s.MarkIntegrationPending(ctx, p.ID, "merged W1", "abc123"); err != nil {
		t.Fatal(err)
	}
	if status, err = s.IntegrationStatus(ctx, p.ID); err != nil || !status.Pending || status.TargetSHA != "abc123" {
		t.Fatalf("merge must leave the branch unverified: %+v err=%v", status, err)
	}
	// A failed integration check does not clear the flag.
	if err = s.RecordIntegrationResult(ctx, p.ID, "abc123", "build_passed: FAILED", false); err != nil {
		t.Fatal(err)
	}
	if status, err = s.IntegrationStatus(ctx, p.ID); err != nil || !status.Pending || status.LastPassed {
		t.Fatalf("a failed integration check stays pending: %+v err=%v", status, err)
	}
	if err = s.RecordIntegrationResult(ctx, p.ID, "def456", "", true); err != nil {
		t.Fatal(err)
	}
	if status, err = s.IntegrationStatus(ctx, p.ID); err != nil || status.Pending || !status.LastPassed || status.LastSHA != "def456" {
		t.Fatalf("a passing integration check clears it: %+v err=%v", status, err)
	}
}
