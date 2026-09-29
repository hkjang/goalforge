package observer

import (
	"context"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// verifiedWork puts an item through to DONE with a commit and a passing gate,
// which is the state a merge approval is about.
func verifiedWork(t *testing.T, ctx context.Context, db *store.Store, goalID, standardID, checkType, sha string, passed bool) model.WorkItem {
	t.Helper()
	item := suppliedItem(t, ctx, db, goalID, standardID, "web/**", 5000)
	if err := db.StartRun(ctx, store.RunRecord{ID: "RUN-" + item.ID, ProjectID: "PRJ-1",
		WorkItemID: item.ID, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordRunCommit(ctx, store.RunCommit{RunID: "RUN-" + item.ID, ProjectID: "PRJ-1",
		GoalID: goalID, WorkItemID: item.ID, CommitSHA: sha, Branch: "goalforge/" + item.ID,
		FilesCommitted: 2}); err != nil {
		t.Fatal(err)
	}
	status := "PASSED"
	if !passed {
		status = "FAILED"
	}
	if err := db.RecordRunVerification(ctx, store.VerificationRecord{RunID: "RUN-" + item.ID,
		CheckType: checkType, Status: status, ActualValue: "true", Required: true,
		EvidenceKind: "journey", Output: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, "RUN-"+item.ID, "COMPLETED", "READY"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetWorkItemStatus(ctx, goalID, item.ID, "DONE"); err != nil {
		t.Fatal(err)
	}
	return item
}

func mergePolicy() AutonomyPolicy {
	policy := openPolicy()
	policy.AutoMerge = true
	return policy
}

// The thing the operator asked for: verified work goes out without them.
func TestVerifiedWorkIsAutomaticallyApprovedForMerge(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	item := verifiedWork(t, ctx, db, goal.ID, "NET-002", "asset_scan", "aaaaaaaaaaaa", true)
	decisions, err := AutoApproveMerges(ctx, db, "PRJ-1", goal.ID, mergePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 1 || decisions.Approved[0] != item.ID {
		t.Fatalf("approved=%+v refused=%+v", decisions.Approved, decisions.Refused)
	}
	// The approval is real, scoped, and already granted, so `goalforge merge`
	// can consume it exactly as it would a person's.
	consumed, err := db.ConsumeScopedApproval(ctx, "PRJ-1", store.ApprovalMergeBranch, "run-x",
		store.ApprovalScope{WorkItemID: item.ID, SourceBranch: "goalforge/" + item.ID,
			TargetRef: "main", CommitSHA: "aaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if !consumed {
		t.Fatal("the automatic approval must be consumable by the merge command")
	}
}

// Merging work whose gates failed is not automation, it is bypassing the
// thing that makes automation trustworthy.
func TestWorkWhoseGatesFailedIsNotApprovedForMerge(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	item := verifiedWork(t, ctx, db, goal.ID, "NET-002", "asset_scan", "bbbbbbbbbbbb", false)
	decisions, err := AutoApproveMerges(ctx, db, "PRJ-1", goal.ID, mergePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatalf("approved=%+v", decisions.Approved)
	}
	if !strings.Contains(decisions.Refused[item.ID], "게이트") {
		t.Fatalf("refusal=%q", decisions.Refused[item.ID])
	}
}

// Auto-merge is its own switch. Turning on automatic execution is not the same
// decision as turning on automatic release, and someone who wanted the first
// must not silently get the second.
func TestAutoMergeIsItsOwnSwitch(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	verifiedWork(t, ctx, db, goal.ID, "NET-002", "asset_scan", "cccccccccccc", true)
	withoutMerge := openPolicy() // Enabled, but AutoMerge is false.
	decisions, err := AutoApproveMerges(ctx, db, "PRJ-1", goal.ID, withoutMerge)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatal("execution autonomy must not grant release autonomy")
	}
}

// The approval is pinned to the commit it was granted for. Work re-run after
// the approval produces a different commit, and the old approval must not
// carry it out.
func TestAnAutomaticMergeApprovalDoesNotCoverALaterCommit(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	item := verifiedWork(t, ctx, db, goal.ID, "NET-002", "asset_scan", "dddddddddddd", true)
	if _, err := AutoApproveMerges(ctx, db, "PRJ-1", goal.ID, mergePolicy()); err != nil {
		t.Fatal(err)
	}
	// The scoped-approval machinery refuses outright rather than quietly
	// declining, and names both commits: a reviewer told only "not approved"
	// would go looking for an approval that is right there.
	consumed, err := db.ConsumeScopedApproval(ctx, "PRJ-1", store.ApprovalMergeBranch, "run-y",
		store.ApprovalScope{WorkItemID: item.ID, SourceBranch: "goalforge/" + item.ID,
			TargetRef: "main", CommitSHA: "eeeeeeeeeeee"})
	if consumed {
		t.Fatal("an approval for one commit must not release a different one")
	}
	if err == nil {
		t.Fatal("the mismatch must be reported, not silently declined")
	}
	for _, want := range []string{"dddddddddddd", "eeeeeeeeeeee"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name both commits: %v", err)
		}
	}
}

// Work a person wrote is not automation's to release, whatever the envelope
// says about criteria it supplied itself.
func TestHandWrittenWorkIsNotAutomaticallyMerged(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	item, err := db.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "hand written", Status: "BACKLOG", Weight: 1, ChangeScope: "web/**",
		EstimatedTokens: 5000, Risk: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.StartRun(ctx, store.RunRecord{ID: "RUN-H", ProjectID: "PRJ-1", WorkItemID: item.ID,
		Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordRunCommit(ctx, store.RunCommit{RunID: "RUN-H", ProjectID: "PRJ-1", GoalID: goal.ID,
		WorkItemID: item.ID, CommitSHA: "ffffffffffff", Branch: "b", FilesCommitted: 1}); err != nil {
		t.Fatal(err)
	}
	if err = db.FinishRun(ctx, "RUN-H", "COMPLETED", "READY"); err != nil {
		t.Fatal(err)
	}
	if err = db.SetWorkItemStatus(ctx, goal.ID, item.ID, "DONE"); err != nil {
		t.Fatal(err)
	}
	decisions, err := AutoApproveMerges(ctx, db, "PRJ-1", goal.ID, mergePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatalf("approved=%+v", decisions.Approved)
	}
	// Absent from both lists. It is not refused — it is simply not this
	// mechanism's business, and reporting it as refused would tell an operator
	// that automation considered releasing their work and declined.
	if reason, refused := decisions.Refused[item.ID]; refused {
		t.Fatalf("hand-written work is not automation's to weigh at all: %q", reason)
	}
}

// A gate that claims a criterion but did not run on this change has not judged
// it. Releasing on the strength of a gate nobody executed is releasing
// unverified work with a green tick beside it.
func TestAGateThatDidNotRunOnThisChangeBlocksTheMerge(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	// Two gates settle NET-002, and the run only exercised one of them.
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetGateSettles(ctx, "PRJ-1", "offline_journey", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	item := verifiedWork(t, ctx, db, goal.ID, "NET-002", "asset_scan", "222222222222", true)
	decisions, err := AutoApproveMerges(ctx, db, "PRJ-1", goal.ID, mergePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatalf("approved=%+v", decisions.Approved)
	}
	// "Did not run" and "ran and failed" are different problems with different
	// fixes — one is a missing test run, the other is broken code — so the
	// refusal has to say which.
	reason := decisions.Refused[item.ID]
	if !strings.Contains(reason, "offline_journey") || !strings.Contains(reason, "돌지 않은") {
		t.Fatalf("the refusal must name the gate and say it never ran: %q", reason)
	}
}

// The branch a change is going into has to be working. Stacking a release onto
// a default branch that is known broken buries which change broke it, and the
// merge command would refuse it a moment later anyway — approving it first
// just means the refusal arrives after somebody thought it was done.
func TestNothingIsApprovedOntoABrokenBranch(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	verifiedWork(t, ctx, db, goal.ID, "NET-002", "asset_scan", "333333333333", true)
	if err := db.RecordIntegrationResult(ctx, "PRJ-1", "sha-main", "go build ./... failed", false); err != nil {
		t.Fatal(err)
	}
	decisions, err := AutoApproveMerges(ctx, db, "PRJ-1", goal.ID, mergePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatalf("approved=%+v", decisions.Approved)
	}
	if !strings.Contains(decisions.Detail, "통합 검증") {
		t.Fatalf("detail=%q", decisions.Detail)
	}
	// Repairing it lets releases resume.
	if err = db.RecordIntegrationResult(ctx, "PRJ-1", "sha-main2", "", true); err != nil {
		t.Fatal(err)
	}
	if decisions, err = AutoApproveMerges(ctx, db, "PRJ-1", goal.ID, mergePolicy()); err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 1 {
		t.Fatalf("a repaired branch accepts releases again: %+v refused=%+v", decisions.Approved, decisions.Refused)
	}
}

// Every automatic merge approval goes into the same audit chain a person's
// does, so `integrity verify` covers it and nothing about the record degrades
// because a machine made it.
func TestAnAutomaticMergeApprovalIsChained(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	verifiedWork(t, ctx, db, goal.ID, "NET-002", "asset_scan", "111111111111", true)
	if _, err := AutoApproveMerges(ctx, db, "PRJ-1", goal.ID, mergePolicy()); err != nil {
		t.Fatal(err)
	}
	report, err := db.VerifyIntegrity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Intact() {
		t.Fatalf("an automatic approval must leave the chain intact: %+v", report.Findings)
	}
	if report.Entries == 0 {
		t.Fatal("the approval must be in the chain")
	}
}

// "All criteria" and "all scopes" are expressible, because an operator who
// wants the whole loop running should not have to enumerate forty IDs.
func TestTheEnvelopeCanBeOpenedCompletely(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "env_scan", []string{"CFG-001"}); err != nil {
		t.Fatal(err)
	}
	item := suppliedItem(t, ctx, db, goal.ID, "CFG-001", "internal/**", 999999)
	open := AutonomyPolicy{Enabled: true, AllStandards: true, AllScopes: true, DailyLimit: 10}
	decisions, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, open)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 1 || decisions.Approved[0] != item.ID {
		t.Fatalf("approved=%+v refused=%+v", decisions.Approved, decisions.Refused)
	}
}

// Opening the envelope completely does not open the one rule the rest rests
// on. Work nothing can judge stays unapproved, because approving it would mean
// the machine writes code and no gate can say whether it worked.
func TestAFullyOpenEnvelopeStillNeedsSomethingToJudgeTheResult(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	item := suppliedItem(t, ctx, db, goal.ID, "CFG-001", "internal/**", 1000)
	open := AutonomyPolicy{Enabled: true, AllStandards: true, AllScopes: true, DailyLimit: 10, AutoMerge: true}
	decisions, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, open)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatal("no gate settles CFG-001, so nothing could say whether the work succeeded")
	}
	if !strings.Contains(decisions.Refused[item.ID], "정산") {
		t.Fatalf("refusal=%q", decisions.Refused[item.ID])
	}
}
