package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/rrsi"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// configCalibrate measures how much this project's evaluation moves on its
// own, from repeated trials of one unchanged configuration.
func configCalibrate(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("config calibrate", flag.ContinueOnError)
	condition := set.String("condition", "", "노이즈를 측정할 구성의 condition hash")
	label := set.String("label", "", "구성 라벨 (condition 대신)")
	resamples := set.Int("resamples", 2000, "재표본 추출 횟수")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	hash := strings.TrimSpace(*condition)
	if hash == "" {
		if hash, err = conditionForLabel(ctx, s, project.ID, *label); err != nil {
			return err
		}
	}
	trials, err := s.CalibrationTrials(ctx, hash)
	if err != nil {
		return err
	}
	calibration, err := rrsi.Calibrate(trials, *resamples, 7)
	if err != nil {
		// Reported as a refusal rather than a zero band. A band of zero says
		// every difference is real, which is the opposite of what measuring it
		// was for.
		return err
	}
	policy, err := s.SelectionPolicyFor(ctx, project.ID)
	if err != nil {
		return err
	}
	policy.NoiseBand, policy.CalibratedFrom = calibration.Band, calibration.Method
	if err = s.SaveSelectionPolicy(ctx, project.ID, policy); err != nil {
		return err
	}
	fmt.Printf("노이즈 대역 %.2f%%p — %s\n", calibration.Band*100, calibration.Method)
	fmt.Println("이보다 작은 성공률 차이는 구성이 아니라 측정이 움직인 것입니다")
	return nil
}

// conditionForLabel finds the condition hash behind an arm label.
func conditionForLabel(ctx context.Context, s *store.Store, projectID, label string) (string, error) {
	if strings.TrimSpace(label) == "" {
		return "", errors.New("--condition 또는 --label 이 필요합니다")
	}
	summaries, err := s.CompareTrials(ctx, projectID, "")
	if err != nil {
		return "", err
	}
	for _, summary := range summaries {
		if summary.Label == label && summary.ConditionHash != "" {
			return summary.ConditionHash, nil
		}
	}
	return "", fmt.Errorf("%q 라벨의 시행 기록을 찾지 못했습니다", label)
}

// configSelection shows or sets the project's selection policy.
func configSelection(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("config selection", flag.ContinueOnError)
	beta0 := set.Float64("free-cost", -1, "무상으로 허용할 상대 비용 증가 (0.05 = 5%)")
	beta1 := set.Float64("cost-per-gain", -1, "성공률 1 증가당 추가로 허용할 상대 비용")
	minTrials := set.Int("min-trials", -1, "판정에 필요한 최소 시행 수")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	policy, err := s.SelectionPolicyFor(ctx, project.ID)
	if err != nil {
		return err
	}
	changed := false
	if *beta0 >= 0 {
		policy.Beta0, changed = *beta0, true
	}
	if *beta1 >= 0 {
		policy.Beta1, changed = *beta1, true
	}
	if *minTrials >= 0 {
		policy.MinTrials, changed = *minTrials, true
	}
	if changed {
		if err = s.SaveSelectionPolicy(ctx, project.ID, policy); err != nil {
			return err
		}
	}
	if policy.Calibrated() {
		fmt.Printf("노이즈 대역 %.2f%%p — %s\n", policy.NoiseBand*100, policy.CalibratedFrom)
	} else {
		fmt.Println("노이즈 대역이 아직 측정되지 않았습니다 — `goalforge config calibrate --label ...` 을 먼저 돌리세요")
		fmt.Println("측정 전에는 어떤 구성 차이도 개선이라고 판정하지 않습니다")
	}
	fmt.Printf("비용 규칙: 무상 %+.0f%% · 성공률 1%%p 당 %+.1f%% 추가 허용\n",
		policy.Beta0*100, policy.Beta1)
	fmt.Printf("최소 시행 %d회\n", policy.MinTrials)
	return nil
}

// configJudge compares measured configurations under the project's policy.
//
// It replaces reading a sorted table. The table put the highest pass rate at
// the top, which presents whichever configuration drew the best sample as the
// best configuration — and that is true exactly as often as the sample was
// representative.
func configJudge(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("config judge", flag.ContinueOnError)
	incumbentLabel := set.String("incumbent", "", "현재 구성의 라벨 (기본: 시행이 가장 많은 것)")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	policy, err := s.SelectionPolicyFor(ctx, project.ID)
	if err != nil {
		return err
	}
	summaries, err := s.CompareTrials(ctx, project.ID, "")
	if err != nil {
		return err
	}
	measured := make([]rrsi.Measurement, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Label == "(모든 라벨)" || summary.Trials == 0 {
			continue
		}
		measured = append(measured, rrsi.Measurement{Label: summary.Label,
			Score: summary.PassRate / 100, Cost: summary.AverageCostUSD, Trials: summary.Trials})
	}
	if len(measured) < 2 {
		fmt.Println("비교할 구성이 둘 이상 필요합니다")
		return nil
	}
	incumbent, rest, err := splitIncumbent(measured, *incumbentLabel)
	if err != nil {
		return err
	}
	best := incumbent.Score
	for _, entry := range measured {
		if entry.Score > best {
			best = entry.Score
		}
	}
	winner, decisions, found := rrsi.Select(incumbent, rest, best, policy)
	fmt.Printf("현재 구성: %s — 성공률 %.1f%% · 시행 %d회\n\n", incumbent.Label,
		incumbent.Score*100, incumbent.Trials)
	sort.SliceStable(decisions, func(i, j int) bool {
		if verdictOrder(decisions[i].Verdict) != verdictOrder(decisions[j].Verdict) {
			return verdictOrder(decisions[i].Verdict) < verdictOrder(decisions[j].Verdict)
		}
		return decisions[i].ScoreDelta > decisions[j].ScoreDelta
	})
	for _, decision := range decisions {
		fmt.Printf("%s %-24s %s\n", verdictMark(decision.Verdict), decision.Label, decision.Reason)
	}
	fmt.Println()
	if !found {
		fmt.Println("바꿀 근거가 있는 구성이 없습니다 — 현재 구성을 유지합니다")
		return nil
	}
	fmt.Printf("채택 후보: %s (성공률 %.1f%%)\n", winner.Label, winner.Score*100)
	return nil
}

// splitIncumbent separates the configuration in place from the candidates.
//
// Without an explicit choice the most-measured configuration is taken to be
// the one in place: it has had the most chances to fail. When several are tied
// on that, the answer is ambiguous and the command refuses — every verdict
// below is a comparison *against* the incumbent, so picking one arbitrarily
// would silently decide what the whole report is measured from.
func splitIncumbent(measured []rrsi.Measurement, label string) (rrsi.Measurement, []rrsi.Measurement, error) {
	index := -1
	if label != "" {
		for i, entry := range measured {
			if entry.Label == label {
				index = i
				break
			}
		}
		if index < 0 {
			return rrsi.Measurement{}, nil, fmt.Errorf("%q 구성의 시행 기록이 없습니다", label)
		}
	} else {
		most, tied := 0, []string{}
		for _, entry := range measured {
			if entry.Trials > most {
				most, tied = entry.Trials, []string{entry.Label}
			} else if entry.Trials == most {
				tied = append(tied, entry.Label)
			}
		}
		if len(tied) > 1 {
			return rrsi.Measurement{}, nil, fmt.Errorf(
				"현재 구성을 정할 수 없습니다 — %s 가 모두 %d회로 같습니다. `--incumbent 라벨` 로 지정하세요",
				strings.Join(tied, ", "), most)
		}
		for i, entry := range measured {
			if entry.Label == tied[0] {
				index = i
				break
			}
		}
	}
	rest := make([]rrsi.Measurement, 0, len(measured)-1)
	rest = append(rest, measured[:index]...)
	rest = append(rest, measured[index+1:]...)
	return measured[index], rest, nil
}

func verdictOrder(verdict string) int {
	switch verdict {
	case rrsi.VerdictBetter:
		return 0
	case rrsi.VerdictNoWorse:
		return 1
	case rrsi.VerdictNotShown:
		return 2
	}
	return 3
}

func verdictMark(verdict string) string {
	switch verdict {
	case rrsi.VerdictBetter:
		return "[v]"
	case rrsi.VerdictNoWorse:
		return "[=]"
	case rrsi.VerdictNotShown:
		return "[ ]"
	}
	return "[?]"
}
