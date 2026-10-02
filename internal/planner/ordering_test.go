package planner

import (
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func scored(title string, contribution float64) Candidate {
	// A real path, not the title with spaces in it. The scope is compared
	// against file paths, so a fixture that put a sentence there was encoding
	// the assumption this package now refuses.
	scope := "internal/" + strings.ReplaceAll(title, " ", "_") + ".go"
	return Candidate{Title: title, ExpectedChangeScope: scope, Risk: "low",
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

// An idea whose scope is a sentence is refused before it is filed.
//
// The scope is compared against file paths, so a sentence matches nothing —
// and an item whose scope matches nothing fails its implementation run with
// "changed files outside declared scope", every time, however good the idea
// was. Discovered by running the chain against a real provider: every item
// `ideas` produced had a prose scope and every implementation run was refused.
func TestAnIdeaWithAProseScopeIsRefusedWithItsReason(t *testing.T) {
	policy := Policy{MaxNewIdeas: 3, MaxUnimplemented: 10, DuplicateThreshold: .72, LowScoreThreshold: 35}
	prose := Candidate{Title: "POST /shorten 핸들러", Risk: "low",
		ExpectedChangeScope: "handler.go 신설: JSON 본문 파싱, 잘못된 입력은 400",
		GoalContribution:    90, UserValue: 90, OperationalNeed: 90, Feasibility: 90, RiskReduction: 90}
	result, err := Discover(policy, nil, []Candidate{prose, scored("rotate api keys", 80)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted) != 1 {
		t.Fatalf("only the well-formed one is admissible: %+v", result.Accepted)
	}
	reason, refused := result.Rejected[prose.Title]
	if !refused {
		t.Fatal("a candidate that cannot run must not be filed")
	}
	// The reason has to say what to write instead. "invalid scope" sends the
	// generator back with nothing to change.
	for _, want := range []string{"경로", "쉼표"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("the refusal must say what form is wanted: %q", reason)
		}
	}
	// And it quotes what it got, truncated, so a paragraph does not fill the
	// report.
	if len(reason) > 200 {
		t.Fatalf("a refusal that reprints a paragraph is unreadable: %q", reason)
	}
}

// A path list is admitted, including a multi-pattern one.
func TestAnIdeaWithAPathListIsAdmitted(t *testing.T) {
	policy := Policy{MaxNewIdeas: 3, MaxUnimplemented: 10, DuplicateThreshold: .72, LowScoreThreshold: 35}
	for _, scope := range []string{"handler.go", "internal/store/**,cmd/app/main.go", "web/src/*.tsx"} {
		candidate := Candidate{Title: "idea " + scope, Risk: "low", ExpectedChangeScope: scope,
			GoalContribution: 90, UserValue: 90, OperationalNeed: 90, Feasibility: 90, RiskReduction: 90}
		result, err := Discover(policy, nil, []Candidate{candidate})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Accepted) != 1 {
			t.Fatalf("%q is a usable scope: %+v", scope, result.Rejected)
		}
	}
}
