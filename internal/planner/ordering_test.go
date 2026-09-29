package planner

import (
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func scored(title string, contribution float64) Candidate {
	return Candidate{Title: title, ExpectedChangeScope: "internal/" + title, Risk: "low",
		GoalContribution: contribution, UserValue: contribution, OperationalNeed: contribution,
		Feasibility: contribution, RiskReduction: contribution}
}

// The defect the ordering has. The cycle limit used to be applied while walking
// the input, so the first N candidates were taken and only then ranked. A
// generator that happens to emit its weakest idea first spends the whole cycle
// budget on it, and the strongest candidate in the batch is rejected with
// "cycle idea limit reached" — the ranking runs on a list the limit already
// decided.
func TestTheBestCandidatesSurviveTheCycleLimit(t *testing.T) {
	policy := Policy{MaxNewIdeas: 2, MaxUnimplemented: 10, DuplicateThreshold: .72, LowScoreThreshold: 35}
	result, err := Discover(policy, nil, []Candidate{
		scored("weakest", 40), scored("weaker", 50), scored("strong", 90), scored("strongest", 95),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted) != 2 {
		t.Fatalf("the cycle limit still holds: %d accepted", len(result.Accepted))
	}
	for i, want := range []string{"strongest", "strong"} {
		if result.Accepted[i].Candidate.Title != want {
			t.Fatalf("accepted[%d]=%q want %q — the limit must cut the weakest, not the last to arrive",
				i, result.Accepted[i].Candidate.Title, want)
		}
	}
	// And the ones that lost must say they lost on rank, not on arrival order.
	if reason := result.Rejected["weakest"]; reason == "" {
		t.Fatal("a candidate that was dropped must say why")
	}
}

// Duplicate detection must still see candidates that the limit later drops.
// Two near-identical proposals in one batch are one idea however many of them
// fit, and admitting both because the ranking ran afterwards would put the same
// work on the board twice.
func TestDuplicatesWithinABatchAreStillCollapsed(t *testing.T) {
	policy := Policy{MaxNewIdeas: 3, MaxUnimplemented: 10, DuplicateThreshold: .72, LowScoreThreshold: 35}
	result, err := Discover(policy, nil, []Candidate{
		scored("add csv export", 90), scored("add csv export", 95), scored("rotate api keys", 80),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted) != 2 {
		t.Fatalf("the duplicate must not be accepted twice: %+v", result.Accepted)
	}
}

// An existing item still blocks a duplicate, whatever it scores.
func TestAnExistingItemStillBlocksADuplicate(t *testing.T) {
	policy := Policy{MaxNewIdeas: 3, MaxUnimplemented: 10, DuplicateThreshold: .72, LowScoreThreshold: 35}
	existing := []model.WorkItem{{ID: "W-1", Title: "add csv export", Status: "BACKLOG"}}
	result, err := Discover(policy, existing, []Candidate{scored("add csv export", 99)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted) != 0 {
		t.Fatalf("a duplicate of existing work is not new work: %+v", result.Accepted)
	}
}
