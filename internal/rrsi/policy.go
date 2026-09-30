// Package rrsi decides whether a measured configuration change is an
// improvement.
//
// GoalForge already records which configuration each run executed under and
// summarizes the pass rate per configuration. What it did with those numbers
// was sort them: highest pass rate first, cheapest as a tiebreak. That
// presents whichever configuration drew the best sample as the best
// configuration, and a sort cannot tell a real gain from the amount an
// unchanged configuration moves on its own.
//
// The rules here are GoalForge's reading of Regularized Recursive
// Self-Improvement of Agent Harnesses (Google Research, Apache-2.0,
// https://github.com/google-research/rrsi, arXiv:2609.24972). The paper's
// subject is evolving an agent harness against a fixed evaluation set, and its
// selection side is the part that applies here: a noise-adjusted floor, a cost
// rule that makes added spend pay for itself, and a shaped rule for changes
// that land inside the noise. The implementation is ours; the discipline is
// theirs.
package rrsi

import (
	"errors"
	"fmt"
	"strings"
)

// Policy is how strict one project is about calling a change an improvement.
//
// It is per project because the two numbers that matter are per project. How
// much an unchanged configuration moves between evaluations depends on that
// project's cases, and how much extra spend a gain is worth depends on what
// the project is for.
type Policy struct {
	// NoiseBand is how far the score of an unchanged configuration moves
	// between evaluations, as a fraction. A difference inside it is the
	// measurement moving, not the configuration.
	//
	// It is zero until somebody measures it. A guessed band is worse than
	// none: it lends the authority of a number to an assumption, and the
	// assumption is invisible in the verdict it produces.
	NoiseBand float64
	// CalibratedFrom records how the band was measured, so a reader can tell
	// an estimate from two evaluations apart from one from two hundred.
	CalibratedFrom string
	// Beta0 is the relative cost increase allowed for free, and Beta1 how much
	// more is allowed per point of score gained. Together they are the price a
	// gain may cost.
	Beta0, Beta1 float64
	// WeightScore, WeightCost and WeightNovelty shape the decision for changes
	// whose gain is inside the noise band. Such a change is not an
	// improvement, but a cheaper or structurally new configuration that is no
	// worse is still worth taking.
	WeightScore, WeightCost, WeightNovelty float64
	// MinTrials is how many trials a measurement needs before it is allowed to
	// decide anything. A configuration that passed two of two has not been
	// shown to beat one that passed a hundred and ninety of two hundred.
	MinTrials int
}

// DefaultPolicy is a starting point, deliberately without a noise band.
//
// Everything else has a defensible default; the band does not, because it is
// the one number that has to come from this project's own measurements.
func DefaultPolicy() Policy {
	return Policy{Beta0: 0.05, Beta1: 2.0, WeightScore: 1.0, WeightCost: 0.5,
		WeightNovelty: 0.02, MinTrials: 20}
}

// Calibrated reports whether the band has been measured.
func (p Policy) Calibrated() bool { return p.NoiseBand > 0 && p.CalibratedFrom != "" }

// ErrNotCalibrated means nobody has measured how much this project's
// evaluation moves on its own, so no difference can be called a gain.
var ErrNotCalibrated = errors.New("평가 노이즈가 아직 측정되지 않았습니다")

// Measurement is one configuration's evaluated result.
type Measurement struct {
	// Label identifies the configuration — GoalForge's condition hash or arm.
	Label string
	// Score is the pass rate as a fraction between 0 and 1.
	Score float64
	// Cost is the average cost of one trial. Relative change is what the cost
	// rule compares, so the unit only has to be consistent.
	Cost   float64
	Trials int
	// NovelComponents counts parts of the configuration this change touched
	// that no accepted change has touched before. A configuration that is no
	// better but exercises something never tried buys information.
	NovelComponents int
}

// Verdicts.
const (
	// VerdictBetter means the gain is larger than the noise band and paid for.
	VerdictBetter = "BETTER"
	// VerdictNoWorse means the difference is inside the noise band and the
	// change is worth taking for its cost or novelty — not because it scored
	// higher.
	VerdictNoWorse = "NO_WORSE"
	// VerdictNotShown means the numbers do not support a change.
	VerdictNotShown = "NOT_SHOWN"
	// VerdictUnjudgeable means the measurement cannot decide anything yet.
	VerdictUnjudgeable = "UNJUDGEABLE"
)

// Decision is the verdict with the arithmetic that produced it.
type Decision struct {
	Label      string
	Verdict    string
	ScoreDelta float64
	// CostDelta is relative: a candidate costing a tenth more is 0.1.
	CostDelta float64
	Reason    string
}

// Adopt reports whether this decision supports replacing the incumbent.
func (d Decision) Adopt() bool { return d.Verdict == VerdictBetter || d.Verdict == VerdictNoWorse }

func percent(value float64) string { return fmt.Sprintf("%.1f%%p", value*100) }

func ratio(value float64) string { return fmt.Sprintf("%+.0f%%", value*100) }

func join(parts ...string) string { return strings.Join(parts, " · ") }
