package observer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/rrsi"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func flowFixture(t *testing.T) (context.Context, *store.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = db.CreateProject(ctx, model.Project{ID: "PRJ-1", Name: "p", RepositoryPath: "/r",
		DefaultBranch: "main", Provider: "codex", Model: "sonnet", WIPLimit: 1}); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}

// The rule the whole proposal side rests on is that an explanation tested and
// found wanting is not drawn again. That rule reads the history, and the
// history is only useful if the ordinary path writes to it.
//
// It did not. Both places that recorded a proposal recorded only refusals, and
// Falsified() requires a record that was measured — so the falsified set was
// empty however many ideas had been tried and failed, and the search could
// redraw the same one forever.
func TestAnAcceptedProposalThatFailedIsRecordedAsFalsified(t *testing.T) {
	ctx, db := flowFixture(t)
	edits := []rrsi.Edit{{Component: "concurrency",
		Hypothesis: "동시 실행을 늘리면 처리량이 오른다", Detail: "wip 1 → 3",
		Change: &rrsi.Change{Field: "wip_limit", From: "1", To: "3"}}}
	proposalID, err := db.RecordProposal(ctx, "PRJ-1", rrsi.Record{Round: 0, Label: "c1", Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := db.ApplyChangeFor(ctx, "PRJ-1", proposalID, 0, "concurrency",
		rrsi.Change{Field: "wip_limit", From: "1", To: "3"})
	if err != nil {
		t.Fatal(err)
	}
	// The measurement did not support it. The setting goes back and — the part
	// that was missing — the proposal is recorded as having been tested.
	if _, err = db.SettleChange(ctx, applied.ID, store.Settlement{Verdict: rrsi.VerdictNotShown,
		Detail: "노이즈 안"}); err != nil {
		t.Fatal(err)
	}
	history, err := db.ProposalHistory(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history=%+v", history)
	}
	if !history[0].Measured() {
		t.Fatalf("an accepted proposal that was measured is not a screen refusal: %+v", history[0])
	}
	falsified := history.Falsified()
	if len(falsified["concurrency"]) != 1 {
		t.Fatalf("the explanation was tested and did not hold: %+v", falsified)
	}
	// And the screen now refuses to draw it again, which is the whole point.
	refusals := rrsi.Screen(rrsi.ScreenInput{Budget: 2, History: history, Edits: edits})
	if rrsi.Passed(refusals) {
		t.Fatal("the same explanation must not be proposed again")
	}
}

// A change the measurement supported is recorded as accepted, so the same
// explanation is not treated as falsified when it is tried elsewhere.
func TestAProposalThatHeldIsRecordedAsAccepted(t *testing.T) {
	ctx, db := flowFixture(t)
	edits := []rrsi.Edit{{Component: "concurrency", Hypothesis: "동시 실행을 늘리면 처리량이 오른다",
		Detail: "wip 1 → 2"}}
	proposalID, err := db.RecordProposal(ctx, "PRJ-1", rrsi.Record{Round: 0, Label: "c1", Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := db.ApplyChangeFor(ctx, "PRJ-1", proposalID, 0, "concurrency",
		rrsi.Change{Field: "wip_limit", From: "1", To: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SettleChange(ctx, applied.ID, store.Settlement{Verdict: rrsi.VerdictBetter,
		Detail: "성공률 12%p", Score: 0.72, ScoreDelta: 0.12}); err != nil {
		t.Fatal(err)
	}
	history, err := db.ProposalHistory(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if !history[0].Accepted {
		t.Fatalf("a verdict that supported the change accepts the proposal: %+v", history[0])
	}
	if len(history.Falsified()) != 0 {
		t.Fatalf("an explanation that held is not falsified: %+v", history.Falsified())
	}
	// The score trajectory is built from accepted proposals, so a stall can be
	// seen. Before this it was empty whatever happened.
	trajectory, err := db.ScoreTrajectory(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(trajectory) != 1 {
		t.Fatalf("trajectory=%v", trajectory)
	}
}

// A change applied without a proposal behind it settles normally. Requiring
// one would stop a person adjusting a setting by hand.
func TestAChangeWithNoProposalStillSettles(t *testing.T) {
	ctx, db := flowFixture(t)
	applied, err := db.ApplyChange(ctx, "PRJ-1", 0, "concurrency",
		rrsi.Change{Field: "wip_limit", From: "1", To: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SettleChange(ctx, applied.ID, store.Settlement{Verdict: rrsi.VerdictBetter, Detail: "ok"}); err != nil {
		t.Fatal(err)
	}
	history, err := db.ProposalHistory(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("nothing proposed it, so there is nothing to record: %+v", history)
	}
}

// A proposal recorded when it was drafted has not been measured. Reading it as
// measured-and-not-accepted files it as falsified before anything ran, which
// would make the first draft of an idea the reason never to draft it again.
func TestAProposalNotYetJudgedIsNotTreatedAsFalsified(t *testing.T) {
	ctx, db := flowFixture(t)
	edits := []rrsi.Edit{{Component: "concurrency", Hypothesis: "동시 실행을 늘리면 처리량이 오른다",
		Detail: "wip 1 → 3"}}
	if _, err := db.RecordProposal(ctx, "PRJ-1", rrsi.Record{Round: 0, Edits: edits}); err != nil {
		t.Fatal(err)
	}
	history, err := db.ProposalHistory(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if history[0].Measured() {
		t.Fatal("nothing has been measured yet")
	}
	if !history[0].Pending() {
		t.Fatalf("it is waiting on a measurement: %+v", history[0])
	}
	if len(history.Falsified()) != 0 {
		t.Fatalf("an untested explanation is not a refuted one: %+v", history.Falsified())
	}
	// It still counts as having been reached for, so exploration does not send
	// the next round back to the same component as if it were untouched.
	if tried := history.Tried(); len(tried) != 1 || tried[0] != "concurrency" {
		t.Fatalf("tried=%v", tried)
	}
}

// The draft path is only automatic if the proposal carries the value to set.
// Without it the operator retypes the field and the value into another
// command, and the retyping is where the two stop matching.
func TestAnAuthoredProposalCarriesTheApplicableChange(t *testing.T) {
	edits, err := parseProposal(`{"edits":[{"component":"concurrency",
	 "hypothesis":"동시 실행을 늘리면 처리량이 오른다","detail":"wip 1 → 3",
	 "change":{"field":"wip_limit","to":"3"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !edits[0].Applicable() {
		t.Fatal("the proposal named a field and a value")
	}
	if edits[0].Change.Field != "wip_limit" || edits[0].Change.To != "3" {
		t.Fatalf("change=%+v", edits[0].Change)
	}
	// The starting point is not taken from the proposal. The store reads the
	// fact and refuses a change whose "from" has moved under it.
	if edits[0].Change.From != "" {
		t.Fatalf("the proposer must not supply the current value: %q", edits[0].Change.From)
	}
}

// A change naming no field, or no value, is prose claiming to be applicable.
func TestAnEmptyChangeIsNotApplicable(t *testing.T) {
	for _, payload := range []string{
		`{"edits":[{"component":"gate","hypothesis":"게이트를 추가하면 회귀를 잡는다","detail":"x","change":{"field":"","to":"3"}}]}`,
		`{"edits":[{"component":"gate","hypothesis":"게이트를 추가하면 회귀를 잡는다","detail":"x","change":{"field":"wip_limit","to":"  "}}]}`,
		`{"edits":[{"component":"gate","hypothesis":"게이트를 추가하면 회귀를 잡는다","detail":"x"}]}`,
	} {
		edits, err := parseProposal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if edits[0].Applicable() {
			t.Fatalf("nothing automation can apply: %s", payload)
		}
	}
}
