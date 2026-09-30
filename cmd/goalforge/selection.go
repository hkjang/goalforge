package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/model"
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

// configDirection tells the operator what the search should try next.
func configDirection(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("config direction", flag.ContinueOnError)
	round := set.Int("round", -1, "이번 회차 (기본: 기록된 회차 다음)")
	total := set.Int("rounds", 10, "계획한 전체 회차")
	minEdits := set.Int("min-edits", 1, "회차당 최소 편집 한도")
	maxEdits := set.Int("max-edits", 4, "회차당 최대 편집 한도")
	window := set.Int("window", 3, "정체를 판단할 회차 수")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, policy, history, err := proposalContext(ctx, s)
	if err != nil {
		return err
	}
	trajectory, err := s.ScoreTrajectory(ctx, project.ID)
	if err != nil {
		return err
	}
	current := *round
	if current < 0 {
		current = nextRound(history)
	}
	direction := rrsi.Next(history, trajectory, current, *total, *minEdits, *maxEdits, *window, policy.NoiseBand)
	fmt.Printf("%d/%d 회차 — %s\n", current, *total, direction.Summary)
	if len(direction.Avoid) == 0 {
		return nil
	}
	fmt.Println("\n이미 시험해서 성립하지 않은 설명:")
	components := make([]string, 0, len(direction.Avoid))
	for component := range direction.Avoid {
		components = append(components, component)
	}
	sort.Strings(components)
	for _, component := range components {
		for _, hypothesis := range direction.Avoid[component] {
			fmt.Printf("  %s: %s\n", component, hypothesis)
		}
	}
	return nil
}

// configPropose screens a candidate before any evaluation is spent on it.
func configPropose(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("config propose", flag.ContinueOnError)
	round := set.Int("round", -1, "이번 회차")
	label := set.String("label", "", "후보 라벨")
	editSpecs := multiFlag{}
	set.Var(&editSpecs, "edit", "구성:가설[:설명] 형식 (여러 번 지정 가능)")
	budget := set.Int("budget", 0, "편집 한도 (0 이면 회차에서 계산)")
	total := set.Int("rounds", 10, "계획한 전체 회차")
	record := set.Bool("record", false, "심사 결과를 이력에 남긴다")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, policy, history, err := proposalContext(ctx, s)
	if err != nil {
		return err
	}
	_ = policy
	edits, err := parseEdits(editSpecs)
	if err != nil {
		return err
	}
	current := *round
	if current < 0 {
		current = nextRound(history)
	}
	limit := *budget
	if limit <= 0 {
		limit = rrsi.EditBudget(current, *total, 1, 4)
	}
	names, err := s.EvaluationCaseNames(ctx, project.ID)
	if err != nil {
		return err
	}
	refusals := rrsi.Screen(rrsi.ScreenInput{Edits: edits, Budget: limit, CaseNames: names, History: history})
	if rrsi.Passed(refusals) {
		fmt.Printf("심사 통과 — 편집 %d개 (한도 %d) · 새 구조 구성 %d개\n",
			len(edits), limit, history.NovelComponents(edits))
		fmt.Println("측정하고 `goalforge config judge` 로 판정하세요")
		return nil
	}
	fmt.Println(rrsi.Explain(refusals))
	if *record {
		// Recorded without a measurement. It counts as having reached for
		// those components and does not count as having falsified anything —
		// a wall of refusals is a feedback loop, not a set of failures.
		if err = s.RecordProposal(ctx, project.ID, rrsi.Record{Round: current, Label: *label,
			Edits: edits, ScreenRefusal: refusals[0].Kind}); err != nil {
			return err
		}
	}
	return fmt.Errorf("심사에서 거절되었습니다 (%d건) — 평가 비용을 쓰기 전에 고치세요", len(refusals))
}

func proposalContext(ctx context.Context, s *store.Store) (model.Project, rrsi.Policy, rrsi.History, error) {
	project, err := currentProject(ctx, s)
	if err != nil {
		return project, rrsi.Policy{}, nil, err
	}
	policy, err := s.SelectionPolicyFor(ctx, project.ID)
	if err != nil {
		return project, policy, nil, err
	}
	history, err := s.ProposalHistory(ctx, project.ID)
	return project, policy, history, err
}

func nextRound(history rrsi.History) int {
	highest := -1
	for _, record := range history {
		if record.Round > highest {
			highest = record.Round
		}
	}
	return highest + 1
}

// parseEdits reads component:hypothesis[:detail] specifications.
func parseEdits(specs []string) ([]rrsi.Edit, error) {
	if len(specs) == 0 {
		return nil, errors.New("--edit 이 하나 이상 필요합니다")
	}
	edits := make([]rrsi.Edit, 0, len(specs))
	for _, spec := range specs {
		parts := strings.SplitN(spec, ":", 3)
		if len(parts) < 2 {
			return nil, fmt.Errorf("%q 는 구성:가설[:설명] 형식이 아닙니다", spec)
		}
		edit := rrsi.Edit{Component: strings.TrimSpace(parts[0]), Hypothesis: strings.TrimSpace(parts[1])}
		if len(parts) == 3 {
			edit.Detail = strings.TrimSpace(parts[2])
		}
		edits = append(edits, edit)
	}
	return edits, nil
}

// multiFlag collects a flag given more than once.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ", ") }

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
