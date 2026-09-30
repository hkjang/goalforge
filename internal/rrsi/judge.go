package rrsi

import "fmt"

// Judge decides whether a candidate configuration should replace the
// incumbent.
//
// best is the highest score any configuration has reached, not the
// incumbent's. Measuring the floor against the best ever seen is what stops a
// run from walking downhill one insignificant step at a time: each step is
// inside the band against the step before it, and after ten of them the
// configuration is materially worse than where it started.
func Judge(incumbent, candidate Measurement, best float64, policy Policy) Decision {
	decision := Decision{Label: candidate.Label}
	if !policy.Calibrated() {
		decision.Verdict = VerdictUnjudgeable
		decision.Reason = "평가 노이즈를 먼저 측정해야 합니다 — 측정이 저절로 얼마나 움직이는지 모르면 어떤 차이도 개선이라고 부를 수 없습니다"
		return decision
	}
	if policy.MinTrials > 0 && (candidate.Trials < policy.MinTrials || incumbent.Trials < policy.MinTrials) {
		// Two of two passing has not been shown to beat a hundred and ninety
		// of two hundred. Ranking them together would let the smallest sample
		// win by having had the fewest chances to fail.
		decision.Verdict = VerdictUnjudgeable
		decision.Reason = fmt.Sprintf("시행이 부족합니다: 후보 %d회 · 기존 %d회 (최소 %d회)",
			candidate.Trials, incumbent.Trials, policy.MinTrials)
		return decision
	}
	decision.ScoreDelta = candidate.Score - incumbent.Score
	decision.CostDelta = relativeChange(candidate.Cost, incumbent.Cost)
	floor := best - policy.NoiseBand
	if candidate.Score < floor {
		decision.Verdict = VerdictNotShown
		decision.Reason = fmt.Sprintf("지금까지 최고 %.1f%% 에서 노이즈 %s 를 빼도 %.1f%% 에 못 미칩니다",
			best*100, percent(policy.NoiseBand), candidate.Score*100)
		return decision
	}
	if decision.ScoreDelta > policy.NoiseBand {
		budget := policy.Beta0 + policy.Beta1*decision.ScoreDelta
		if decision.CostDelta > budget {
			// The gain is real and too expensive. Reported separately from a
			// gain that is not real, because the answer to one is to make it
			// cheaper and the answer to the other is to stop.
			decision.Verdict = VerdictNotShown
			decision.Reason = fmt.Sprintf("성공률은 %s 올랐지만 비용이 %s 로 허용치 %s 를 넘습니다",
				percent(decision.ScoreDelta), ratio(decision.CostDelta), ratio(budget))
			return decision
		}
		decision.Verdict = VerdictBetter
		decision.Reason = join(
			fmt.Sprintf("성공률 %s (노이즈 %s 초과)", percent(decision.ScoreDelta), percent(policy.NoiseBand)),
			fmt.Sprintf("비용 %s (허용 %s)", ratio(decision.CostDelta), ratio(budget)))
		return decision
	}
	// Inside the band. Whatever the score did, it did not do it measurably —
	// so the only reasons left to take this change are that it costs less or
	// exercises something nothing has exercised before.
	shaped := policy.WeightScore*decision.ScoreDelta - policy.WeightCost*decision.CostDelta +
		policy.WeightNovelty*float64(candidate.NovelComponents)
	if shaped <= 0 {
		decision.Verdict = VerdictNotShown
		decision.Reason = fmt.Sprintf("성공률 차이 %s 가 노이즈 %s 안이고, 비용 %s 로 상쇄되지 않습니다",
			percent(decision.ScoreDelta), percent(policy.NoiseBand), ratio(decision.CostDelta))
		return decision
	}
	decision.Verdict = VerdictNoWorse
	decision.Reason = fmt.Sprintf("성공률 차이 %s 는 노이즈 %s 안이라 개선이 아닙니다 — %s",
		percent(decision.ScoreDelta), percent(policy.NoiseBand), noWorseReason(decision, candidate))
	return decision
}

// noWorseReason names what actually carried a within-band change, rather than
// reciting every term. Reporting "비용 +0% 때문에" for a change that was taken
// on its score movement tells the reader the wrong thing about why.
func noWorseReason(decision Decision, candidate Measurement) string {
	var reasons []string
	if decision.CostDelta < 0 {
		reasons = append(reasons, fmt.Sprintf("비용이 %s 줄어", ratio(decision.CostDelta)))
	}
	if candidate.NovelComponents > 0 {
		reasons = append(reasons, fmt.Sprintf("처음 건드리는 구성이 %d개라", candidate.NovelComponents))
	}
	if len(reasons) == 0 {
		// Nothing but the unmeasurable score movement, at no extra cost. Worth
		// taking, and the reader should know that is all it was.
		return "추가 비용 없이 소폭이나마 올라 받아들입니다"
	}
	return join(reasons...) + " 받아들입니다"
}

// Select judges every candidate and returns the one to adopt, if any.
//
// Among admissible candidates the highest score wins, which is only safe
// because everything that reached this point already cleared the floor and the
// cost rule. Sorting by score without those two is the thing this package
// exists to stop.
func Select(incumbent Measurement, candidates []Measurement, best float64, policy Policy) (Measurement, []Decision, bool) {
	decisions := make([]Decision, 0, len(candidates))
	var winner Measurement
	found := false
	for _, candidate := range candidates {
		decision := Judge(incumbent, candidate, best, policy)
		decisions = append(decisions, decision)
		if !decision.Adopt() {
			continue
		}
		if !found || candidate.Score > winner.Score {
			winner, found = candidate, true
		}
	}
	return winner, decisions, found
}

// relativeChange is how much bigger the candidate's cost is, as a fraction of
// the incumbent's.
//
// An incumbent costing nothing has no meaningful relative change, and dividing
// by it would make every candidate infinitely expensive. Zero is the honest
// answer: with no baseline spend there is nothing for the cost rule to weigh.
func relativeChange(candidate, incumbent float64) float64 {
	if incumbent <= 0 {
		return 0
	}
	return (candidate - incumbent) / incumbent
}
