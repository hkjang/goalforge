package sqlite

import (
	"context"
	"testing"
)

func mergeEffect(t *testing.T, ctx context.Context, s *Store, projectID, workID, state string) {
	t.Helper()
	key := EffectKey(EffectMergeBranch, projectID, workID, "main", "abc123")
	effect := ExternalEffect{ID: NewID("EFF"), ProjectID: projectID, WorkItemID: workID,
		Kind: EffectMergeBranch, Key: key, Target: "main", Branch: "work/" + workID, RequestHash: "abc123",
		State: EffectIntended}
	if _, _, err := s.BeginEffect(ctx, effect); err != nil {
		t.Fatal(err)
	}
	if state != EffectIntended {
		if err := s.SettleEffect(ctx, key, state, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func cardFor(t *testing.T, board Board, id string) BoardCard {
	t.Helper()
	for _, column := range board.Columns {
		for _, card := range column.Cards {
			if card.Item.ID == id {
				return card
			}
		}
	}
	t.Fatalf("%s is not on the board", id)
	return BoardCard{}
}

// The whole reason the badge exists. Merging something does not make it
// shippable: the branches have to hold up together, and when they do not, a
// DONE column that reads as "released" is the screen telling the operator the
// opposite of the truth.
func TestMergedWorkWithFailedIntegrationIsNotReleasable(t *testing.T) {
	ctx, s, project, goal := boardFixture(t)
	seedItem(t, ctx, s, goal, "W-1", "merged", "DONE", 1)
	mergeEffect(t, ctx, s, project.ID, "W-1", EffectSucceeded)
	if err := s.RecordIntegrationResult(ctx, project.ID, "sha1", "build broke on main", false); err != nil {
		t.Fatal(err)
	}
	board, err := s.BoardFor(ctx, project.ID, goal, BoardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	card := cardFor(t, board, "W-1")
	if card.Item.Status != "DONE" {
		t.Fatalf("the work item's own verification stands: %s", card.Item.Status)
	}
	if card.Delivery.State == DeliveryReleasable {
		t.Fatal("a merge whose integration check failed must not read as releasable")
	}
	if card.Delivery.State != DeliveryIntegrationFailed || !card.Delivery.Blocking {
		t.Fatalf("delivery=%+v", card.Delivery)
	}
	if card.Delivery.Detail == "" {
		t.Fatal("a blocking badge with no reason sends the reader hunting")
	}
}

// The ladder, rung by rung. Each state is a different answer to "may I ship
// this", and collapsing any two of them loses a distinction someone acts on.
func TestDeliveryBadgeDistinguishesEachStage(t *testing.T) {
	ctx, s, project, goal := boardFixture(t)
	seedItem(t, ctx, s, goal, "W-done", "verified only", "DONE", 1)
	seedItem(t, ctx, s, goal, "W-wait", "awaiting approval", "DONE", 1)
	seedItem(t, ctx, s, goal, "W-merging", "merging", "DONE", 1)
	seedItem(t, ctx, s, goal, "W-failed", "merge failed", "DONE", 1)
	seedItem(t, ctx, s, goal, "W-ship", "releasable", "DONE", 1)
	if _, err := s.RequestScopedApproval(ctx, project.ID, ApprovalMergeBranch, "review me",
		ApprovalScope{WorkItemID: "W-wait", SourceBranch: "work/W-wait", TargetRef: "main", CommitSHA: "abc123"}); err != nil {
		t.Fatal(err)
	}
	mergeEffect(t, ctx, s, project.ID, "W-merging", EffectIntended)
	mergeEffect(t, ctx, s, project.ID, "W-failed", EffectFailed)
	mergeEffect(t, ctx, s, project.ID, "W-ship", EffectSucceeded)
	if err := s.RecordIntegrationResult(ctx, project.ID, "sha1", "", true); err != nil {
		t.Fatal(err)
	}
	board, err := s.BoardFor(ctx, project.ID, goal, BoardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"W-done": DeliveryVerified, "W-wait": DeliveryAwaitingApproval, "W-merging": DeliveryMerging,
		"W-failed": DeliveryMergeFailed, "W-ship": DeliveryReleasable,
	} {
		if got := cardFor(t, board, id).Delivery.State; got != want {
			t.Fatalf("%s: delivery=%s want %s", id, got, want)
		}
	}
	if label := DeliveryLabel(DeliveryReleasable); label == DeliveryReleasable {
		t.Fatal("the board shows the label, so every state needs one")
	}
}

// A project that has never verified its merged result cannot claim the check
// passed. Reporting "releasable" from the absence of a failure would be
// asserting a check nobody ran.
func TestMergeWithoutAnyIntegrationCheckIsNotReleasable(t *testing.T) {
	ctx, s, project, goal := boardFixture(t)
	seedItem(t, ctx, s, goal, "W-1", "merged", "DONE", 1)
	mergeEffect(t, ctx, s, project.ID, "W-1", EffectSucceeded)
	board, err := s.BoardFor(ctx, project.ID, goal, BoardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cardFor(t, board, "W-1").Delivery.State; got != DeliveryIntegrationPending {
		t.Fatalf("delivery=%s", got)
	}
}

// Work still being done carries no delivery badge: there is nothing to say
// about shipping something that has not finished.
func TestUnfinishedWorkCarriesNoDeliveryBadge(t *testing.T) {
	ctx, s, project, goal := boardFixture(t)
	seedItem(t, ctx, s, goal, "W-1", "in flight", "IN_PROGRESS", 1)
	board, err := s.BoardFor(ctx, project.ID, goal, BoardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if state := cardFor(t, board, "W-1").Delivery.State; state != "" {
		t.Fatalf("delivery=%s", state)
	}
}
