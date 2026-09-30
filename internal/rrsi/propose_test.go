package rrsi

import (
	"math"
	"strings"
	"testing"
)

func edit(component, hypothesis string) Edit {
	return Edit{Component: component, Hypothesis: hypothesis, Detail: "changed " + component}
}

func record(round int, verdict string, accepted bool, delta float64, edits ...Edit) Record {
	return Record{Round: round, Label: "cand", Edits: edits, Verdict: verdict,
		Accepted: accepted, ScoreDelta: delta}
}

// The whole reason the history is kept. A search that does not remember which
// explanations were tested keeps drawing the most plausible one — and the most
// plausible one is exactly what gets tried first and fails first.
func TestAFalsifiedHypothesisIsNotDrawnAgain(t *testing.T) {
	history := History{
		record(0, VerdictNotShown, false, -0.01, edit("prompt", "실패 사유를 더 자세히 주면 재시도가 성공한다")),
	}
	refusals := Screen(ScreenInput{Budget: 2, History: history,
		Edits: []Edit{edit("prompt", "실패 사유를 더 자세히 주면 재시도가 성공한다")}})
	if Passed(refusals) {
		t.Fatal("an explanation that was tested and did not hold must not be redrawn")
	}
	if refusals[0].Kind != RefusalFalsified {
		t.Fatalf("refusals=%+v", refusals)
	}
	// Wording is not the identity of an idea: the same hypothesis with
	// different spacing and case is the same hypothesis.
	reworded := Screen(ScreenInput{Budget: 2, History: history,
		Edits: []Edit{edit("prompt", "실패 사유를   더 자세히 주면 재시도가 성공한다")}})
	if Passed(reworded) {
		t.Fatal("respacing a falsified hypothesis does not make it a new one")
	}
	// A different explanation on the same component is fine.
	different := Screen(ScreenInput{Budget: 2, History: history,
		Edits: []Edit{edit("prompt", "인수 조건을 먼저 보여 주면 범위를 덜 벗어난다")}})
	if !Passed(different) {
		t.Fatalf("a new explanation is not refused: %s", Explain(different))
	}
}

// One failure does not falsify a hypothesis that has also been confirmed.
// Treating it as falsified would retire an explanation that works, on the
// strength of the one case where it did not.
func TestAConfirmedHypothesisSurvivesAFailureElsewhere(t *testing.T) {
	history := History{
		record(0, VerdictBetter, true, 0.05, edit("gate", "여정 게이트를 붙이면 회귀를 잡는다")),
		record(1, VerdictNotShown, false, -0.01, edit("gate", "여정 게이트를 붙이면 회귀를 잡는다")),
	}
	if len(history.Falsified()["gate"]) != 0 {
		t.Fatalf("falsified=%+v", history.Falsified())
	}
}

// A change that mentions an evaluation case scores better without the product
// being better, and the score is the thing that would have caught it.
func TestAChangeThatNamesAnEvaluationCaseIsRefused(t *testing.T) {
	refusals := Screen(ScreenInput{Budget: 2, CaseNames: []string{"csv-export", "login-flow"},
		Edits: []Edit{edit("prompt", "csv-export 사례에서 컬럼 순서를 먼저 확인하게 한다")}})
	if Passed(refusals) {
		t.Fatal("tuning to the measured cases must be refused before it is measured")
	}
	if refusals[0].Kind != RefusalLeakage {
		t.Fatalf("refusals=%+v", refusals)
	}
	if !strings.Contains(refusals[0].Detail, "csv-export") {
		t.Fatalf("the refusal must name what leaked: %q", refusals[0].Detail)
	}
	// The same change without naming a case is fine.
	general := Screen(ScreenInput{Budget: 2, CaseNames: []string{"csv-export", "login-flow"},
		Edits: []Edit{edit("prompt", "출력 형식이 있는 작업에서는 형식을 먼저 확인하게 한다")}})
	if !Passed(general) {
		t.Fatalf("a general change is not leakage: %s", Explain(general))
	}
}

// A case called "api" would refuse every change that mentions an API. The
// screen is worth having only while its refusals are about leakage — one bad
// refusal teaches people to pass a flag.
func TestAGenericCaseNameIsNotScreenedOn(t *testing.T) {
	refusals := Screen(ScreenInput{Budget: 2, CaseNames: []string{"api", "ui"},
		Edits: []Edit{edit("prompt", "api 응답을 먼저 확인하게 한다")}})
	if !Passed(refusals) {
		t.Fatalf("a three-letter case name is too generic to screen on: %s", Explain(refusals))
	}
}

// A change with no stated expectation cannot be falsified, and a search that
// cannot falsify anything revisits the same idea until the budget runs out.
func TestAnEditWithoutAHypothesisIsRefused(t *testing.T) {
	refusals := Screen(ScreenInput{Budget: 2,
		Edits: []Edit{{Component: "prompt", Detail: "reworded the template"}}})
	if Passed(refusals) {
		t.Fatal("an edit that says what it changes and not what it expects must be refused")
	}
	if refusals[0].Kind != RefusalNoHypothesis {
		t.Fatalf("refusals=%+v", refusals)
	}
}

// Naming a component the taxonomy does not have makes every proposal novel by
// inventing a new word for what it touched.
func TestAnUnknownComponentIsRefused(t *testing.T) {
	refusals := Screen(ScreenInput{Budget: 2,
		Edits: []Edit{edit("vibes", "느낌을 좋게 하면 성공률이 오른다")}})
	if Passed(refusals) {
		t.Fatal("an invented component name must be refused")
	}
}

// A secret pasted into a configuration change is caught by a screen that runs
// rather than a rule somebody is supposed to remember.
func TestACredentialInAChangeIsRefused(t *testing.T) {
	refusals := Screen(ScreenInput{Budget: 2,
		Edits: []Edit{{Component: "model", Hypothesis: "다른 제공자를 쓰면 빠르다",
			Detail: `api_key = "sk-abcdefghijklmnopqrstuvwxyz0123"`}}})
	if Passed(refusals) {
		t.Fatal("a credential in a change must be refused")
	}
	found := false
	for _, refusal := range refusals {
		if refusal.Kind == RefusalSecret {
			found = true
		}
	}
	if !found {
		t.Fatalf("refusals=%+v", refusals)
	}
}

// Every objection comes back at once. A repair that fixes one only to hit the
// next wastes a round per objection.
func TestEveryObjectionIsReportedTogether(t *testing.T) {
	refusals := Screen(ScreenInput{Budget: 1, CaseNames: []string{"csv-export"},
		Edits: []Edit{
			edit("prompt", "csv-export 를 특별히 다룬다"),
			{Component: "gate", Detail: "no hypothesis"},
		}})
	kinds := map[string]bool{}
	for _, refusal := range refusals {
		kinds[refusal.Kind] = true
	}
	for _, want := range []string{RefusalBudget, RefusalLeakage, RefusalNoHypothesis} {
		if !kinds[want] {
			t.Fatalf("%s missing from %s", want, Explain(refusals))
		}
	}
}

// Early rounds may bundle coordinated changes; late rounds are sparse so the
// measurement says which edit it is about.
func TestTheEditBudgetAnnealsToOne(t *testing.T) {
	first := EditBudget(0, 10, 1, 4)
	if first != 4 {
		t.Fatalf("the first round gets the whole budget: %d", first)
	}
	// The cosine reaches the minimum at t = T, so the last round of a T-round
	// run is near it rather than at it. Asserting the last round is exactly
	// one would be asserting a schedule this is not.
	if last := EditBudget(9, 10, 1, 4); last != 2 {
		t.Fatalf("round 9 of 10 is near the floor: %d", last)
	}
	if end := EditBudget(10, 10, 1, 4); end != 1 {
		t.Fatalf("the floor is reached at t = T: %d", end)
	}
	previous := first
	for round := 1; round < 10; round++ {
		current := EditBudget(round, 10, 1, 4)
		if current > previous {
			t.Fatalf("the budget must not widen: round %d gave %d after %d", round, current, previous)
		}
		previous = current
	}
	// A run with no declared length has no schedule to anneal along.
	if EditBudget(0, 0, 1, 4) != 4 {
		t.Fatal("without a round count the budget is the maximum")
	}
}

// A run that has not had time to move is not stalled, and telling it that it
// is would redirect it before it had tried anything.
func TestARunIsNotStalledBeforeItHasHadTime(t *testing.T) {
	if Stalled([]float64{0.80, 0.81}, 3, 0.02) {
		t.Fatal("two rounds cannot show three rounds of no movement")
	}
	if !Stalled([]float64{0.80, 0.80, 0.81, 0.81}, 3, 0.02) {
		t.Fatal("one point over three rounds on a two-point band is a stall")
	}
	if Stalled([]float64{0.80, 0.80, 0.81, 0.90}, 3, 0.02) {
		t.Fatal("ten points is not a stall")
	}
}

// When the run is stalled and something has never been tried, exploring it is
// the only move that can produce information the run does not already have.
func TestAStalledRunIsSentSomewhereItHasNotBeen(t *testing.T) {
	history := History{record(0, VerdictNotShown, false, 0, edit("prompt", "a"))}
	direction := Next(history, []float64{0.80, 0.80, 0.80, 0.80}, 5, 10, 1, 4, 3, 0.02)
	if !direction.Stalled {
		t.Fatalf("direction=%+v", direction)
	}
	if len(direction.Explore) == 0 || contains(direction.Explore, "prompt") {
		t.Fatalf("explore must be components nothing reached for: %v", direction.Explore)
	}
	if !strings.Contains(direction.Summary, "정체") {
		t.Fatalf("summary=%q", direction.Summary)
	}
	// Not stalled: untried components are still reported, but not as a
	// directive. A run that is moving does not need redirecting.
	moving := Next(history, []float64{0.70, 0.80, 0.90, 0.95}, 5, 10, 1, 4, 3, 0.02)
	if len(moving.Explore) != 0 {
		t.Fatalf("a moving run is not redirected: %v", moving.Explore)
	}
	if !strings.Contains(moving.Summary, "아직 안 건드린") {
		t.Fatalf("summary=%q", moving.Summary)
	}
}

// A refused candidate still counts as having reached for its component.
// Directing the run there again as "untried" would send it back to something
// it has already been told it cannot do.
func TestARefusedCandidateStillCountsAsTried(t *testing.T) {
	history := History{{Round: 0, Edits: []Edit{edit("gate", "a")}, ScreenRefusal: "LEAKAGE"}}
	if !contains(history.Tried(), "gate") {
		t.Fatalf("tried=%v", history.Tried())
	}
	if contains(history.Untried(), "gate") {
		t.Fatalf("untried=%v", history.Untried())
	}
	// But it is not evidence about whether the component helps: a wall of
	// refusals is a feedback loop, not a set of failures.
	if !math.IsInf(history.RecentBestGain("gate", 10), -1) {
		t.Fatal("a refused candidate measured nothing")
	}
	// And its hypothesis was never tested, so it is not falsified. Refusing to
	// redraw it would retire an idea on the strength of a screen that never
	// let it run.
	if len(history.Falsified()["gate"]) != 0 {
		t.Fatalf("a screened-out candidate falsified nothing: %+v", history.Falsified())
	}
	screened := Screen(ScreenInput{Budget: 2, History: history,
		Edits: []Edit{edit("gate", "a")}})
	if !Passed(screened) {
		t.Fatalf("the idea may be proposed again once the leakage is gone: %s", Explain(screened))
	}
}

// A component pushed on repeatedly without gain is machinery the search is
// maintaining for nothing. Naming it is what lets the next proposal remove
// rather than extend it.
func TestComponentsThatStoppedHelpingArePruned(t *testing.T) {
	history := History{
		record(0, VerdictBetter, true, 0.05, edit("gate", "a")),
		record(1, VerdictNotShown, false, -0.01, edit("context", "b")),
		record(2, VerdictNotShown, false, -0.02, edit("context", "c")),
	}
	prune := history.PruneSet(10)
	if !contains(prune, "context") {
		t.Fatalf("prune=%v", prune)
	}
	if contains(prune, "gate") {
		t.Fatal("a component that gained must not be pruned")
	}
	if contains(prune, "model") {
		t.Fatal("a component nothing has tried has not stopped helping")
	}
}

// Novelty counts structural components no accepted proposal has touched, and
// only those: a setting nobody changed is not a capability nobody has.
func TestNoveltyCountsStructuralComponentsOnly(t *testing.T) {
	history := History{record(0, VerdictBetter, true, 0.05, edit("gate", "a"))}
	if history.NovelComponents([]Edit{edit("gate", "b")}) != 0 {
		t.Fatal("gate already has an accepted edit")
	}
	if history.NovelComponents([]Edit{edit("context", "b")}) != 1 {
		t.Fatal("context is structural and untouched")
	}
	if history.NovelComponents([]Edit{edit("model", "b")}) != 0 {
		t.Fatal("a model setting is not a capability gap")
	}
	if history.NovelComponents([]Edit{edit("context", "b"), edit("context", "c")}) != 1 {
		t.Fatal("two edits on one component are one novel component")
	}
}
