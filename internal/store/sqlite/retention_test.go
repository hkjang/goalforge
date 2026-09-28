package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
)

func retentionFixture(t *testing.T) (context.Context, *Store, model.Project, model.Goal) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, project.ID, "G", "o", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, s, project, goal
}

// seedFinishedRun records a run with a bulky event, a prompt, and a piece of
// verification evidence, then ends it at the given time.
func seedFinishedRun(t *testing.T, ctx context.Context, s *Store, project model.Project, goal model.Goal, runID string, endedAt time.Time) {
	t.Helper()
	if err := s.StartRun(ctx, RunRecord{ID: runID, ProjectID: project.ID, Provider: "codex", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPrompt(ctx, runID, "work_item_execution", "아주 긴 프롬프트 본문입니다"); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"type": "message", "text": "매우 긴 제공자 이벤트 본문"})
	if err := s.RecordProviderEvent(ctx, project.ID, provider.Event{RunID: runID, Type: provider.EventMessage,
		TurnID: runID + "-t1", Raw: payload}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRunVerification(ctx, VerificationRecord{RunID: runID, CheckType: "build_passed",
		Status: "PASSED", ActualValue: "true", Required: true, Output: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, runID, "COMPLETED", "READY"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE runs SET ended_at=? WHERE id=?`, endedAt.UTC().Format(time.RFC3339Nano), runID); err != nil {
		t.Fatal(err)
	}
}

func countNonEmpty(t *testing.T, ctx context.Context, s *Store, query string) int64 {
	t.Helper()
	var count int64
	if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// The bodies are what grow; the record that something happened is what the
// audit is for. Pruning drops the first and keeps the second.
func TestPruneDropsBodiesAndKeepsTheRecord(t *testing.T) {
	ctx, s, project, goal := retentionFixture(t)
	seedFinishedRun(t, ctx, s, project, goal, "RUN-OLD", time.Now().Add(-60*24*time.Hour))
	report, err := s.Prune(ctx, time.Now().Add(-30*24*time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	if report.EventBodies != 1 || report.PromptBodies != 1 {
		t.Fatalf("the old run's bodies must be covered: %+v", report)
	}
	if report.Reclaimable() == 0 {
		t.Fatal("the report must say how much it covers")
	}
	// The rows survive: what happened and when is still answerable.
	if rows := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM event_logs`); rows != 1 {
		t.Fatalf("event rows must survive: %d", rows)
	}
	if rows := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM prompt_records`); rows != 1 {
		t.Fatalf("prompt rows must survive: %d", rows)
	}
	// The hash survives with the row, so the fingerprint of what was said is
	// still on record even though the words are not.
	if hashes := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM event_logs WHERE raw_hash<>''`); hashes != 1 {
		t.Fatalf("the event fingerprint must survive: %d", hashes)
	}
	// The bodies are gone.
	if bodies := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM event_logs WHERE length(raw_payload)>0`); bodies != 0 {
		t.Fatalf("event bodies must be gone: %d", bodies)
	}
	if bodies := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM prompt_records WHERE length(redacted_prompt)>0`); bodies != 0 {
		t.Fatalf("prompt bodies must be gone: %d", bodies)
	}
}

// Evidence and approvals are what a goal's completion rests on, and the
// integrity chain covers them: removing one would make `integrity verify`
// report tampering, correctly and unhelpfully.
func TestPruneNeverTouchesEvidenceOrTheChain(t *testing.T) {
	ctx, s, project, goal := retentionFixture(t)
	seedFinishedRun(t, ctx, s, project, goal, "RUN-OLD", time.Now().Add(-90*24*time.Hour))
	approval, err := s.RequestApproval(ctx, project.ID, ApprovalProtectedFiles, "오래된 승인")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Approve(ctx, project.ID, approval.ID); err != nil {
		t.Fatal(err)
	}
	evidenceBefore := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM verification_results`)
	chainBefore := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM audit_chain`)
	if _, err = s.Prune(ctx, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if after := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM verification_results`); after != evidenceBefore {
		t.Fatalf("evidence must survive pruning: %d -> %d", evidenceBefore, after)
	}
	if after := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM audit_chain`); after != chainBefore {
		t.Fatalf("the chain must survive pruning: %d -> %d", chainBefore, after)
	}
	// The whole point: the records still verify after a prune.
	report, err := s.VerifyIntegrity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Intact() {
		t.Fatalf("pruning must not break the integrity chain: %+v", report.Findings)
	}
}

// A run that has not ended is still using its own record.
func TestPruneLeavesRunningWorkAlone(t *testing.T) {
	ctx, s, project, _ := retentionFixture(t)
	if err := s.StartRun(ctx, RunRecord{ID: "RUN-LIVE", ProjectID: project.ID, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPrompt(ctx, "RUN-LIVE", "t", "살아 있는 프롬프트"); err != nil {
		t.Fatal(err)
	}
	report, err := s.Prune(ctx, time.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	if report.PromptBodies != 0 {
		t.Fatalf("an unfinished run must be left alone: %+v", report)
	}
	if bodies := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM prompt_records WHERE length(redacted_prompt)>0`); bodies != 1 {
		t.Fatalf("the live prompt must survive: %d", bodies)
	}
}

// Reporting without removing is the default, because audit data deleted on a
// typo does not come back.
func TestDryRunReportsAndRemovesNothing(t *testing.T) {
	ctx, s, project, goal := retentionFixture(t)
	seedFinishedRun(t, ctx, s, project, goal, "RUN-OLD", time.Now().Add(-60*24*time.Hour))
	report, err := s.Prune(ctx, time.Now().Add(-30*24*time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Applied || report.EventBodies != 1 {
		t.Fatalf("a dry run must report what it would do: %+v", report)
	}
	if bodies := countNonEmpty(t, ctx, s, `SELECT COUNT(*) FROM event_logs WHERE length(raw_payload)>0`); bodies != 1 {
		t.Fatalf("a dry run must remove nothing: %d", bodies)
	}
}

// A cutoff in the future would take runs that are still being verified.
func TestFutureCutoffIsRefused(t *testing.T) {
	ctx, s, _, _ := retentionFixture(t)
	if _, err := s.Prune(ctx, time.Now().Add(time.Hour), true); err == nil {
		t.Fatal("a future cutoff must be refused")
	}
}

// Recent runs are the ones most likely to be needed, so a cutoff must not
// reach past it.
func TestRecentRunsAreNotPruned(t *testing.T) {
	ctx, s, project, goal := retentionFixture(t)
	seedFinishedRun(t, ctx, s, project, goal, "RUN-NEW", time.Now().Add(-time.Hour))
	report, err := s.Prune(ctx, time.Now().Add(-30*24*time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Empty() {
		t.Fatalf("a run from an hour ago is not older than 30 days: %+v", report)
	}
}

// The breakdown exists so pruning is a decision rather than a ritual.
func TestStorageSaysWhereTheSpaceWent(t *testing.T) {
	ctx, s, project, goal := retentionFixture(t)
	for i := 0; i < 3; i++ {
		seedFinishedRun(t, ctx, s, project, goal, fmt.Sprintf("RUN-%d", i), time.Now().Add(-time.Hour))
	}
	breakdown, err := s.Storage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if breakdown.Total <= 0 {
		t.Fatal("the database has a size")
	}
	byTable := map[string]StorageRow{}
	for _, row := range breakdown.Rows {
		byTable[row.Table] = row
	}
	if byTable["event_logs"].Rows != 3 || byTable["event_logs"].Bytes <= 0 {
		t.Fatalf("event bodies must be measured: %+v", byTable["event_logs"])
	}
	if byTable["audit_chain"].Rows == 0 {
		t.Fatalf("the chain must be listed so its cost is visible too: %+v", byTable["audit_chain"])
	}
}
