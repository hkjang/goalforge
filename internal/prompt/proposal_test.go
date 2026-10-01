package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/rrsi"
)

func historyWith(records ...rrsi.Record) rrsi.History { return rrsi.History(records) }

func proposed(component, hypothesis, verdict string, accepted bool, delta float64) rrsi.Record {
	return rrsi.Record{Edits: []rrsi.Edit{{Component: component, Hypothesis: hypothesis, Detail: "d"}},
		Verdict: verdict, Accepted: accepted, ScoreDelta: delta}
}

// Everything that makes this a regularized search rather than a guess is in
// the context. A proposer given only the configuration and "make it better"
// draws the most plausible idea, which is the one tried first and failed first.
func TestTheProposalCarriesWhatWasAlreadyTried(t *testing.T) {
	history := historyWith(
		proposed("prompt", "실패 사유를 자세히 주면 재시도가 성공한다", rrsi.VerdictNotShown, false, -0.01),
		proposed("gate", "여정 게이트를 붙이면 회귀를 잡는다", rrsi.VerdictBetter, true, 0.06),
	)
	direction := rrsi.Next(history, []float64{0.80, 0.86}, 2, 10, 1, 4, 3, 0.02)
	text := Proposal(direction, history, "model=sonnet wip=1", 12)
	for _, want := range []string{
		"실패 사유를 자세히 주면 재시도가 성공한다", // what was tried
		"개선을 보이지 못함",              // and what it measured
		"여정 게이트를 붙이면 회귀를 잡는다",
		"채택됨",
		"다시 제시하지 말 것", // the falsified set, named as such
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the proposal must carry %q:\n%s", want, text)
		}
	}
	// The round's budget reaches the proposer as a number, not as advice.
	if !strings.Contains(text, "최대 3개") && !strings.Contains(text, "최대 4개") {
		t.Fatalf("the edit budget must be stated:\n%s", text)
	}
}

// A wall of screen refusals is a feedback loop — the proposer reads its own
// rejected attempts and writes more of the same. The measurements are the only
// part that says anything about the product.
func TestRefusedAttemptsAreSummarizedNotRecited(t *testing.T) {
	var records []rrsi.Record
	for i := 0; i < 6; i++ {
		records = append(records, rrsi.Record{
			Edits:         []rrsi.Edit{{Component: "prompt", Hypothesis: "거절된 아이디어", Detail: "d"}},
			ScreenRefusal: "LEAKAGE"})
	}
	records = append(records, proposed("gate", "진짜 측정된 것", rrsi.VerdictBetter, true, 0.05))
	history := historyWith(records...)
	text := Proposal(rrsi.Next(history, nil, 1, 10, 1, 4, 3, 0.02), history, "cfg", 5)
	if strings.Count(text, "거절된 아이디어") > 0 {
		t.Fatalf("refused attempts must not be recited back:\n%s", text)
	}
	if !strings.Contains(text, "거절되어 측정되지 않은 제안 6건") {
		t.Fatalf("but their number is still said:\n%s", text)
	}
	if !strings.Contains(text, "진짜 측정된 것") {
		t.Fatal("the measured one is what matters")
	}
}

// A stalled run is told where it has not been, and that it must go there.
func TestAStalledRunIsGivenADirective(t *testing.T) {
	history := historyWith(proposed("prompt", "a", rrsi.VerdictNotShown, false, 0))
	direction := rrsi.Next(history, []float64{0.80, 0.80, 0.80, 0.80}, 5, 10, 1, 4, 3, 0.02)
	text := Proposal(direction, history, "cfg", 5)
	if !strings.Contains(text, "반드시 건드려야 한다") {
		t.Fatalf("a stalled run gets a directive, not a suggestion:\n%s", text)
	}
	// A moving run does not get one.
	moving := rrsi.Next(history, []float64{0.70, 0.80, 0.90, 0.95}, 5, 10, 1, 4, 3, 0.02)
	if strings.Contains(Proposal(moving, history, "cfg", 5), "반드시 건드려야 한다") {
		t.Fatal("a run that is moving must not be redirected")
	}
}

// The hypothesis is required by the schema rather than asked for in prose. A
// model given an optional field for "why" will sometimes fill it, and an edit
// without one cannot be falsified.
func TestTheSchemaRequiresAFalsifiableHypothesis(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(ProposalSchema()), &schema); err != nil {
		t.Fatal(err)
	}
	items := schema["properties"].(map[string]any)["edits"].(map[string]any)["items"].(map[string]any)
	required, _ := items["required"].([]any)
	found := map[string]bool{}
	for _, entry := range required {
		found[entry.(string)] = true
	}
	for _, key := range []string{"component", "hypothesis", "detail"} {
		if !found[key] {
			t.Fatalf("%q must be required by the schema: %v", key, required)
		}
	}
	// And the component is constrained to the known taxonomy: an invented name
	// makes every proposal look novel.
	properties := items["properties"].(map[string]any)
	component := properties["component"].(map[string]any)
	if component["enum"] == nil {
		t.Fatal("the component must be an enum, or a proposer can invent one")
	}
	// A hypothesis of "." satisfies a string field but not a minimum length.
	hypothesis := properties["hypothesis"].(map[string]any)
	if hypothesis["minLength"] == nil {
		t.Fatal("a one-character hypothesis is not a hypothesis")
	}
}

// The proposer is told not to tune to the evaluation set, and told how many
// cases there are — a rule with no stake behind it reads as boilerplate.
func TestTheProposerIsWarnedOffTheEvaluationSet(t *testing.T) {
	text := Proposal(rrsi.Direction{Budget: 2, Summary: "s"}, nil, "cfg", 12)
	if !strings.Contains(text, "평가 사례는 12개") {
		t.Fatalf("the warning must name the stake:\n%s", text)
	}
	if !strings.Contains(text, "측정 전에 거절된다") {
		t.Fatalf("and say what happens:\n%s", text)
	}
}
