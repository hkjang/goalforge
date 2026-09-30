package observer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func autonomyFixture(t *testing.T) (context.Context, *store.Store, standards.Pack, model.Goal) {
	t.Helper()
	ctx, db, goal := supplyFixture(t)
	pack := standards.GoReactOfflineService()
	profile := standards.Profile{ProjectID: "PRJ-1", PackRef: pack.Ref(),
		Attributes: map[string]string{"frontend": "react", "network": "offline", "deployment": "service"}}
	if err := db.SaveStandardProfile(ctx, profile, pack); err != nil {
		t.Fatal(err)
	}
	return ctx, db, pack, goal
}

var suppliedCount int

// suppliedItem files a distinct finding each time. Two items for the same
// criterion, defect and scope are the same finding, and the dedup key refuses
// the second — correctly, which is why the defect kind varies here.
func suppliedItem(t *testing.T, ctx context.Context, db *store.Store, goalID, standardID, scope string, tokens int64) model.WorkItem {
	t.Helper()
	suppliedCount++
	defect := fmt.Sprintf("defect_%d", suppliedCount)
	item, err := db.CreateWorkItem(ctx, model.WorkItem{GoalID: goalID, Type: "IMPLEMENT",
		Title: standardID + ": something", Status: "BACKLOG", Weight: 1, ChangeScope: scope,
		EstimatedTokens: tokens, Risk: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.FileFinding(ctx, "PRJ-1", standardID, defect, scope, item.ID); err != nil {
		t.Fatal(err)
	}
	return item
}

func openPolicy() AutonomyPolicy {
	return AutonomyPolicy{Enabled: true, AllowedStandards: []string{"NET-002"},
		AllowedScopes: []string{"web/**"}, MaxTokens: 20000, DailyLimit: 5}
}

// The rule the whole thing rests on. Approving work nothing can judge means
// the machine writes code and no gate can say whether it worked — the change
// lands as "done" on the strength of having been attempted.
func TestWorkNothingCanJudgeIsNotAutoApproved(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	item := suppliedItem(t, ctx, db, goal.ID, "NET-002", "web/**", 5000)
	decisions, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatal("no gate claims NET-002, so nothing can say whether the work succeeded")
	}
	if !strings.Contains(decisions.Refused[item.ID], "정산") {
		t.Fatalf("the refusal must say why: %q", decisions.Refused[item.ID])
	}
	// With a gate that settles it, the same item is approvable.
	if err = db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	decisions, err = AutoApprove(ctx, db, "PRJ-1", goal.ID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 1 || decisions.Approved[0] != item.ID {
		t.Fatalf("approved=%+v refused=%+v", decisions.Approved, decisions.Refused)
	}
	after, err := db.WorkItemByID(ctx, goal.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "APPROVED" {
		t.Fatalf("status=%s", after.Status)
	}
}

// Autonomy is off unless somebody turned it on. A default that runs is a
// default nobody chose.
func TestAutonomyIsOffByDefault(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	suppliedItem(t, ctx, db, goal.ID, "NET-002", "web/**", 5000)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	// Everything else about the policy permits this item, so the only thing
	// that can refuse it is the switch itself.
	off := openPolicy()
	off.Enabled = false
	decisions, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, off)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatal("autonomy that is not enabled must approve nothing")
	}
	if decisions.Detail == "" {
		t.Fatal("a pass that did nothing because it is switched off must say so")
	}
	// And an empty allowlist is an allowlist: switched on with nothing allowed
	// still approves nothing.
	empty := AutonomyPolicy{Enabled: true, DailyLimit: 5}
	if decisions, err = AutoApprove(ctx, db, "PRJ-1", goal.ID, empty); err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatal("an empty allowlist allows nothing")
	}
}

// The envelope is what the operator agreed to. Work outside it waits for a
// person, and each dimension is checked separately so the refusal says which
// one it was.
func TestWorkOutsideTheEnvelopeWaitsForAPerson(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetGateSettles(ctx, "PRJ-1", "env_scan", []string{"CFG-001"}); err != nil {
		t.Fatal(err)
	}
	outsideCriterion := suppliedItem(t, ctx, db, goal.ID, "CFG-001", "web/**", 5000)
	outsideScope := suppliedItem(t, ctx, db, goal.ID, "NET-002", "internal/**", 5000)
	tooBig := suppliedItem(t, ctx, db, goal.ID, "NET-002", "web/**", 999999)
	decisions, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatalf("approved=%+v", decisions.Approved)
	}
	for id, want := range map[string]string{
		outsideCriterion.ID: "기준", outsideScope.ID: "범위", tooBig.ID: "크기",
	} {
		if !strings.Contains(decisions.Refused[id], want) {
			t.Fatalf("%s: refusal must name the dimension %q: %q", id, want, decisions.Refused[id])
		}
	}
}

// A risky item is not made safe by sitting inside the envelope. The envelope
// says where automation may act, not that everything there is harmless.
func TestHighRiskWorkIsNotAutoApproved(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	item, err := db.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "NET-002: risky", Status: "BACKLOG", Weight: 1, ChangeScope: "web/**",
		EstimatedTokens: 5000, Risk: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.FileFinding(ctx, "PRJ-1", "NET-002", "defect", "web/**", item.ID); err != nil {
		t.Fatal(err)
	}
	decisions, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatalf("a high-risk item must wait for a person: %+v", decisions.Approved)
	}
	if !strings.Contains(decisions.Refused[item.ID], "위험") {
		t.Fatalf("refusal=%q", decisions.Refused[item.ID])
	}
}

// Work a person did not supply is not the supplier's to approve. Automation
// given a standing approval for its own findings must not inherit one for
// everything on the board.
func TestOnlySuppliedWorkIsAutoApproved(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	// Created by hand, not filed against a criterion.
	handwritten, err := db.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: "IMPLEMENT",
		Title: "rewrite the whole thing", Status: "BACKLOG", Weight: 1, ChangeScope: "web/**",
		EstimatedTokens: 5000, Risk: "low"})
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatalf("hand-written work is not the supplier's to approve: %+v", decisions.Approved)
	}
	if _, refused := decisions.Refused[handwritten.ID]; refused {
		t.Fatal("it is not refused either — it is simply not this mechanism's business")
	}
}

// The day's allowance is spent whether or not the work then succeeded. A cap
// that only counted successes would let a loop that fails every time run
// without limit.
func TestTheDailyAllowanceIsSpentOnEveryApproval(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		suppliedItem(t, ctx, db, goal.ID, "NET-002", "web/**", 1000)
	}
	policy := openPolicy()
	policy.DailyLimit = 2
	first, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Approved) != 2 {
		t.Fatalf("approved=%+v", first.Approved)
	}
	second, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Approved) != 0 {
		t.Fatalf("the day's allowance is spent: %+v", second.Approved)
	}
	if second.Detail == "" {
		t.Fatal("a spent allowance must be said, not read as nothing to do")
	}
}

// Every automatic approval is recorded with what allowed it. An approval
// nobody can trace back to a rule is indistinguishable from one nobody made.
func TestAnAutomaticApprovalIsRecorded(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	item := suppliedItem(t, ctx, db, goal.ID, "NET-002", "web/**", 5000)
	if _, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, openPolicy()); err != nil {
		t.Fatal(err)
	}
	records, err := db.AutoApprovals(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%+v", records)
	}
	if records[0].WorkItemID != item.ID || records[0].StandardID != "NET-002" {
		t.Fatalf("record=%+v", records[0])
	}
	for _, want := range []string{"NET-002", "web/**", "asset_scan"} {
		if !strings.Contains(records[0].Basis, want) {
			t.Fatalf("the basis must name %q: %q", want, records[0].Basis)
		}
	}
}

// Autonomy does not extend to sending anything outward. The merge boundary is
// a person's, and a supplier that could approve its own merges would be the
// only reviewer of its own work.
func TestAutonomyDoesNotReachTheMergeBoundary(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	item := suppliedItem(t, ctx, db, goal.ID, "NET-002", "web/**", 5000)
	if _, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, openPolicy()); err != nil {
		t.Fatal(err)
	}
	pending, err := db.ListPendingApprovals(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, approval := range pending {
		if approval.ActionType == store.ApprovalMergeBranch {
			t.Fatal("automation must not raise or grant its own merge approval")
		}
	}
	consumed, err := db.ConsumeScopedApproval(ctx, "PRJ-1", store.ApprovalMergeBranch, "run-1",
		store.ApprovalScope{WorkItemID: item.ID, SourceBranch: "b", TargetRef: "main", CommitSHA: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if consumed {
		t.Fatal("there must be no merge approval to consume")
	}
}

// A failed attempt is not retried automatically. Whatever stopped it is still
// there, and a second identical attempt spends budget to reach the same place.
func TestAFailedAttemptIsNotAutomaticallyRetried(t *testing.T) {
	ctx, db, _, goal := autonomyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	item := suppliedItem(t, ctx, db, goal.ID, "NET-002", "web/**", 5000)
	if _, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, openPolicy()); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordAutoApprovalOutcome(ctx, item.ID, false, "게이트 asset_scan 실패: 여전히 외부 폰트를 부릅니다"); err != nil {
		t.Fatal(err)
	}
	// Back on the board for a person to look at.
	current, err := db.WorkItemByID(ctx, goal.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ApplyManualTransition(ctx, goal.ID, item.ID, "BACKLOG", current.Version); err != nil {
		t.Fatal(err)
	}
	decisions, err := AutoApprove(ctx, db, "PRJ-1", goal.ID, openPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions.Approved) != 0 {
		t.Fatalf("a failed attempt must not be repeated automatically: %+v", decisions.Approved)
	}
	if !strings.Contains(decisions.Refused[item.ID], "실패") {
		t.Fatalf("refusal=%q", decisions.Refused[item.ID])
	}
	// The reason and the resume point are kept.
	records, err := db.AutoApprovals(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(records[0].Outcome, "여전히 외부 폰트") {
		t.Fatalf("the failure detail must be preserved: %q", records[0].Outcome)
	}
}

var _ = errors.Is
