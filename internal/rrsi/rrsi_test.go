package rrsi

import (
	"errors"
	"strings"
	"testing"
)

func calibrated(band float64) Policy {
	policy := DefaultPolicy()
	policy.NoiseBand, policy.CalibratedFrom = band, "테스트"
	return policy
}

func at(label string, score, cost float64, trials int) Measurement {
	return Measurement{Label: label, Score: score, Cost: cost, Trials: trials}
}

// The rule this package exists for. Sorting configurations by pass rate
// presents whichever one drew the best sample as the best configuration, and a
// sort cannot tell a real gain from the amount an unchanged configuration
// moves on its own.
func TestAGainInsideTheNoiseIsNotAGain(t *testing.T) {
	policy := calibrated(0.03)
	incumbent := at("A", 0.80, 1.0, 100)
	// Two points better on a measurement that moves three points by itself.
	inside := Judge(incumbent, at("B", 0.82, 1.0, 100), 0.80, policy)
	if inside.Verdict == VerdictBetter {
		t.Fatalf("a two-point gain on a three-point band is the measurement moving: %+v", inside)
	}
	if !strings.Contains(inside.Reason, "노이즈") {
		t.Fatalf("reason=%q", inside.Reason)
	}
	// Four points is outside it.
	outside := Judge(incumbent, at("C", 0.84, 1.0, 100), 0.80, policy)
	if outside.Verdict != VerdictBetter {
		t.Fatalf("outside=%+v", outside)
	}
}

// A band nobody measured is not a band. A guessed one lends the authority of a
// number to an assumption, and the assumption is invisible in the verdict it
// produces.
func TestNothingIsJudgedWithoutAMeasuredBand(t *testing.T) {
	uncalibrated := DefaultPolicy()
	if uncalibrated.Calibrated() {
		t.Fatal("the default policy must not pretend to have a band")
	}
	decision := Judge(at("A", 0.80, 1, 100), at("B", 0.95, 1, 100), 0.80, uncalibrated)
	if decision.Verdict != VerdictUnjudgeable {
		t.Fatalf("a fifteen-point gain is still unjudgeable without a band: %+v", decision)
	}
	if decision.Adopt() {
		t.Fatal("an unjudgeable candidate must not be adopted")
	}
	// A band with no provenance is the same as no band: somebody typed it.
	typed := DefaultPolicy()
	typed.NoiseBand = 0.03
	if typed.Calibrated() {
		t.Fatal("a band with no record of how it was measured is a guess")
	}
}

// A hundred percent over two trials has not been shown to beat ninety-five
// over two hundred. Ranking them together lets the smallest sample win by
// having had the fewest chances to fail.
func TestASmallSampleCannotWin(t *testing.T) {
	policy := calibrated(0.02)
	policy.MinTrials = 20
	decision := Judge(at("A", 0.95, 1.0, 200), at("B", 1.0, 1.0, 2), 0.95, policy)
	if decision.Verdict != VerdictUnjudgeable {
		t.Fatalf("decision=%+v", decision)
	}
	if !strings.Contains(decision.Reason, "시행") {
		t.Fatalf("reason=%q", decision.Reason)
	}
}

// A real gain that costs four times as much is a real gain nobody can afford,
// and it is a different problem from a gain that is not real: one is made
// cheaper, the other is abandoned.
func TestARealGainStillHasToBePaidFor(t *testing.T) {
	policy := calibrated(0.02)
	policy.Beta0, policy.Beta1 = 0.05, 2.0
	incumbent := at("A", 0.80, 1.0, 100)
	// Five points for three times the cost: allowed is 0.05 + 2*0.05 = 0.15.
	expensive := Judge(incumbent, at("B", 0.85, 3.0, 100), 0.80, policy)
	if expensive.Verdict != VerdictNotShown {
		t.Fatalf("expensive=%+v", expensive)
	}
	if !strings.Contains(expensive.Reason, "비용") {
		t.Fatalf("the refusal must name the cost: %q", expensive.Reason)
	}
	// The same gain at a tenth more cost is affordable.
	affordable := Judge(incumbent, at("C", 0.85, 1.1, 100), 0.80, policy)
	if affordable.Verdict != VerdictBetter {
		t.Fatalf("affordable=%+v", affordable)
	}
}

// A change inside the band that costs less is worth taking — but it is not an
// improvement, and the verdict has to say which it is or the next reader will
// cite it as one.
func TestACheaperChangeInsideTheBandIsNoWorseNotBetter(t *testing.T) {
	policy := calibrated(0.03)
	decision := Judge(at("A", 0.80, 1.0, 100), at("B", 0.79, 0.5, 100), 0.80, policy)
	if decision.Verdict != VerdictNoWorse {
		t.Fatalf("decision=%+v", decision)
	}
	if !decision.Adopt() {
		t.Fatal("half the cost for no measurable loss is worth taking")
	}
	if !strings.Contains(decision.Reason, "개선이 아닙니다") {
		t.Fatalf("the verdict must not read as an improvement: %q", decision.Reason)
	}
}

// Each step is inside the band against the step before it, and after ten of
// them the configuration is materially worse than where it started. The floor
// is measured against the best ever seen for exactly this reason.
func TestAConfigurationCannotWalkDownhillOneQuietStepAtATime(t *testing.T) {
	policy := calibrated(0.03)
	policy.WeightCost = 0.5
	best := 0.90
	incumbent := at("A", 0.90, 1.0, 100)
	// Each of these is only two points below the one before it.
	for _, score := range []float64{0.88, 0.86, 0.84} {
		decision := Judge(incumbent, at("step", score, 0.5, 100), best, policy)
		if decision.Adopt() && score < best-policy.NoiseBand {
			t.Fatalf("%.2f is below the best ever seen minus the band: %+v", score, decision)
		}
		incumbent = at("A", score, 1.0, 100)
	}
	// And the first step, which is genuinely inside the band, is allowed.
	if decision := Judge(at("A", 0.90, 1.0, 100), at("step", 0.88, 0.5, 100), best, policy); !decision.Adopt() {
		t.Fatalf("two points below on half the cost is inside the band: %+v", decision)
	}
}

// Among candidates that already cleared the floor and the cost rule, the
// highest score wins. Sorting by score without those two is the thing this
// package exists to stop.
func TestSelectionRanksOnlyWhatAlreadyPassedTheRules(t *testing.T) {
	policy := calibrated(0.02)
	policy.MinTrials = 10
	incumbent := at("A", 0.80, 1.0, 100)
	winner, decisions, found := Select(incumbent, []Measurement{
		at("cheap-but-worse", 0.60, 0.1, 100),
		at("best-but-unaffordable", 0.95, 10.0, 100),
		at("good", 0.86, 1.05, 100),
		at("better", 0.88, 1.10, 100),
		at("tiny-sample", 1.0, 1.0, 3),
	}, 0.80, policy)
	if !found || winner.Label != "better" {
		t.Fatalf("winner=%+v found=%v", winner, found)
	}
	byLabel := map[string]Decision{}
	for _, decision := range decisions {
		byLabel[decision.Label] = decision
	}
	if byLabel["best-but-unaffordable"].Adopt() {
		t.Fatal("the highest score must not win by outspending everything")
	}
	if byLabel["tiny-sample"].Verdict != VerdictUnjudgeable {
		t.Fatalf("tiny=%+v", byLabel["tiny-sample"])
	}
	if byLabel["cheap-but-worse"].Adopt() {
		t.Fatal("twenty points below the best is not cheap, it is worse")
	}
}

// A band measured from one run per case would be zero, and a zero band says
// every difference is real — the opposite of what calibration is for, arriving
// with the authority of a measurement.
func TestCalibrationRefusesWithoutRepetitions(t *testing.T) {
	_, err := Calibrate([]CaseTrials{
		{CaseID: "a", Outcomes: []bool{true}},
		{CaseID: "b", Outcomes: []bool{false}},
	}, 500, 7)
	if !errors.Is(err, ErrNotCalibrated) {
		t.Fatalf("one run per case measures nothing about movement: %v", err)
	}
	// Repetitions that all agree also measure nothing about movement.
	_, err = Calibrate([]CaseTrials{
		{CaseID: "a", Outcomes: []bool{true, true, true}},
		{CaseID: "b", Outcomes: []bool{true, true, true}},
	}, 500, 7)
	if !errors.Is(err, ErrNotCalibrated) {
		t.Fatalf("outcomes that never disagree have no measured spread: %v", err)
	}
}

// A real band, measured from repetitions that disagree.
func TestCalibrationMeasuresTheSpreadItIsGiven(t *testing.T) {
	steady := []CaseTrials{
		{CaseID: "a", Outcomes: []bool{true, true, true, false}},
		{CaseID: "b", Outcomes: []bool{true, true, true, false}},
		{CaseID: "c", Outcomes: []bool{true, true, true, false}},
	}
	calm, err := Calibrate(steady, 2000, 7)
	if err != nil {
		t.Fatal(err)
	}
	noisy := []CaseTrials{
		{CaseID: "a", Outcomes: []bool{true, false, true, false}},
		{CaseID: "b", Outcomes: []bool{true, false, true, false}},
		{CaseID: "c", Outcomes: []bool{true, false, true, false}},
	}
	rough, err := Calibrate(noisy, 2000, 7)
	if err != nil {
		t.Fatal(err)
	}
	if rough.Band <= calm.Band {
		t.Fatalf("a configuration that disagrees with itself more has a wider band: %.4f vs %.4f",
			rough.Band, calm.Band)
	}
	// The method says what it was measured over, because a band from three
	// trials and one from three hundred are different claims.
	if calm.Cases != 3 || calm.Trials != 12 {
		t.Fatalf("calibration=%+v", calm)
	}
	if !strings.Contains(calm.Method, "사례 3개") {
		t.Fatalf("method=%q", calm.Method)
	}
	// And the same input gives the same band, so a verdict can be re-derived.
	again, err := Calibrate(steady, 2000, 7)
	if err != nil {
		t.Fatal(err)
	}
	if again.Band != calm.Band {
		t.Fatalf("calibration must be reproducible: %.6f vs %.6f", again.Band, calm.Band)
	}
}

// Cases with too few repetitions are left out rather than counted as steady.
// Folding them in would drag the measured spread toward zero with cases that
// have no spread to contribute.
func TestCasesWithoutRepetitionsAreLeftOut(t *testing.T) {
	calibration, err := Calibrate([]CaseTrials{
		{CaseID: "a", Outcomes: []bool{true, false, true, false}},
		{CaseID: "b", Outcomes: []bool{true}},
		{CaseID: "c", Outcomes: []bool{true, false, true, false}},
	}, 2000, 7)
	if err != nil {
		t.Fatal(err)
	}
	if calibration.Cases != 2 || calibration.Trials != 8 {
		t.Fatalf("calibration=%+v", calibration)
	}
}
