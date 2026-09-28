package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/goalforge/goalforge/internal/evaluation"
	"github.com/goalforge/goalforge/internal/policy"
	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/verification"
)

// BaselineExecutor measures what the same model, with the same tools, in the
// same workspace, under the same budget, achieves on the same task when it is
// simply asked to do it — no decomposition, no verification-repair loop, no
// evidence ledger.
//
// It exists because "GoalForge completes N% of tasks" is not a claim about
// GoalForge unless something else was measured the same way. The only thing
// that may differ between the two arms is the system under test, so the task
// text, the budget, the workspace, and above all the judge are shared.
type BaselineExecutor struct {
	Provider       provider.Provider
	Model          string
	MaxOutputBytes int
}

func (e BaselineExecutor) Execute(ctx context.Context, env evaluation.Environment, spec evaluation.CaseSpec) (evaluation.Outcome, error) {
	var outcome evaluation.Outcome
	if e.Provider == nil {
		return outcome, errors.New("baseline executor needs a provider")
	}
	if len(spec.Criteria) == 0 {
		return outcome, errors.New("evaluation case needs completion criteria; without them nothing can judge the trial")
	}
	if len(spec.Gates) == 0 {
		return outcome, errors.New("evaluation case needs gates; the baseline must be judged by the same checks as GoalForge")
	}
	request := provider.RunRequest{
		RunID:          "baseline-" + spec.CaseID,
		Prompt:         BaselinePrompt(spec),
		WorkDir:        env.Workspace,
		Model:          e.Model,
		GoalObjective:  spec.GoalObjective,
		WorkspaceWrite: true,
		Ephemeral:      true,
	}
	if spec.TokenBudget > 0 {
		budget := spec.TokenBudget
		request.GoalTokenBudget = &budget
	}
	events, err := e.Provider.Start(ctx, request)
	if err != nil {
		return outcome, err
	}
	var failure string
	for event := range events {
		if event.Usage != nil {
			outcome.Tokens += event.Usage.InputTokens + event.Usage.OutputTokens +
				event.Usage.CachedInputTokens + event.Usage.ReasoningTokens
			outcome.CostUSD += event.Usage.CostUSD
		}
		switch {
		case event.Err != nil:
			failure = event.Err.Error()
		case event.Type == provider.EventFailed:
			failure = event.Message
		}
	}
	if ctx.Err() != nil {
		return outcome, ctx.Err()
	}
	// The session is over. From here the baseline is judged exactly as
	// GoalForge is: the same gates, measured the same way, against the same
	// criteria, with the same rule about what kind of evidence settles what.
	completed, detail, judgeErr := e.judge(ctx, env, spec)
	if judgeErr != nil {
		return outcome, judgeErr
	}
	outcome.Completed = completed
	switch {
	case completed:
		outcome.Detail = detail
	case failure != "":
		outcome.Detail = "세션 실패: " + failure
	default:
		outcome.Detail = detail
	}
	return outcome, nil
}

// judge runs the case's gates against whatever the session left behind and
// decides completion by the same rules the product uses.
func (e BaselineExecutor) judge(ctx context.Context, env evaluation.Environment, spec evaluation.CaseSpec) (bool, string, error) {
	db, err := store.Open(env.StateDB)
	if err != nil {
		return false, "", err
	}
	defer db.Close()
	maxOutput := e.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = 64 * 1024
	}
	engine, err := verification.New(db, maxOutput)
	if err != nil {
		return false, "", err
	}
	gates := make([]verification.Gate, 0, len(spec.Gates))
	for _, gate := range spec.Gates {
		timeout := gateTimeout(gate)
		gates = append(gates, verification.Gate{Type: gate.Type, Command: gate.Command, Timeout: timeout,
			Required: gate.Required, SuccessValue: gate.SuccessValue, ValuePattern: gate.ValuePattern, Kind: gate.Kind})
	}
	results, _, err := engine.Check(ctx, env.Workspace, gates)
	if err != nil {
		return false, "", err
	}
	measured := make(map[string]verification.Result, len(results))
	for _, result := range results {
		measured[result.Type] = result
	}
	kinds := make(map[string]string, len(spec.Gates))
	for _, gate := range spec.Gates {
		kinds[gate.Type] = gate.Kind
	}
	var unmet []string
	for _, criterion := range spec.Criteria {
		result, ran := measured[criterion.Type]
		if !ran {
			unmet = append(unmet, criterion.Type+" (측정하는 게이트 없음)")
			continue
		}
		if !policy.EvidenceSatisfies(criterion.RequiredKind, kinds[criterion.Type]) {
			unmet = append(unmet, criterion.Type+" (검증 종류 불일치)")
			continue
		}
		if result.Status != "PASSED" || !store.CriterionMet(criterion.ExpectedValue, result.Value) {
			unmet = append(unmet, fmt.Sprintf("%s (기준 %s, 측정 %s)", criterion.Type, criterion.ExpectedValue, dashIfBlank(result.Value)))
		}
	}
	if len(unmet) > 0 {
		return false, "완료 조건 미충족: " + strings.Join(unmet, ", "), nil
	}
	return true, "모든 완료 조건 충족", nil
}

func dashIfBlank(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// BaselinePrompt states the task the way a person would state it to a coding
// tool: what to achieve, and how it will be checked. It deliberately gives the
// baseline the same information GoalForge has, including the exact gate
// commands — withholding them would measure GoalForge's prompt rather than its
// orchestration, and make the comparison flattering rather than true.
func BaselinePrompt(spec evaluation.CaseSpec) string {
	var b strings.Builder
	b.WriteString("작업: " + spec.GoalTitle + "\n\n")
	if strings.TrimSpace(spec.GoalObjective) != "" {
		b.WriteString(spec.GoalObjective + "\n\n")
	}
	b.WriteString("이 작업은 아래 명령으로 검증됩니다. 모두 통과해야 완료입니다.\n")
	for _, gate := range spec.Gates {
		line := "  - " + strings.Join(gate.Command, " ")
		if !gate.Required {
			line += " (선택)"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n완료 조건:\n")
	for _, criterion := range spec.Criteria {
		line := fmt.Sprintf("  - %s = %s", criterion.Type, criterion.ExpectedValue)
		if criterion.RequiredKind != "" {
			line += fmt.Sprintf(" (%s 종류의 검증으로 확인되어야 함)", criterion.RequiredKind)
		}
		b.WriteString(line + "\n")
	}
	for _, seed := range spec.SeedWork {
		b.WriteString("\n하위 작업: " + seed.Title)
		if seed.Objective != "" {
			b.WriteString(" — " + seed.Objective)
		}
	}
	b.WriteString("\n작업 공간에서 직접 파일을 수정해 작업을 완료하세요.\n")
	return b.String()
}
