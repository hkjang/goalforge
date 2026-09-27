package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/goalforge/goalforge/internal/diagnostics"
	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// PlanCheck is one precondition with the verdict a real run would reach.
//
// BLOCK means a run started now would be refused. WARN means it would proceed
// but something is wrong with the outcome — most often that the goal can never
// be judged complete. Conflating the two would make the preview disagree with
// what actually happens, which is the one thing a preview must not do.
type PlanCheck struct {
	// Level is OK, WARN, or BLOCK.
	Level, Name, Detail string
}

// Plan is what the next run would do, evaluated without doing any of it: the
// item that would be chosen and why the others were passed over, the model,
// the expected cost against remaining budget, the gates that would judge it,
// and every precondition that would stop it. Spending a model call to find out
// that a budget is exhausted or a gate is missing is the expensive way to
// learn it.
type Plan struct {
	ProjectID, ProjectName string
	GoalTitle              string
	WorkItem               *model.WorkItem
	Skipped                []store.SkippedCandidate
	Selection              store.Selection
	SelectionReason        string
	Model                  store.ModelChoice
	Forecast               store.TokenForecast
	EstimatedCostUSD       float64
	Budget                 store.ProjectBudget
	Gates                  []store.GateConfig
	Workspace              string
	Isolated               bool
	Checks                 []PlanCheck
}

// Runnable reports whether a run started now would proceed.
func (p Plan) Runnable() bool {
	for _, check := range p.Checks {
		if check.Level == "BLOCK" {
			return false
		}
	}
	return p.WorkItem != nil
}

func (p *Plan) add(level, name, detail string) {
	p.Checks = append(p.Checks, PlanCheck{Level: level, Name: name, Detail: detail})
}

// Plan evaluates what Continue would do, mutating nothing.
func (s *Service) Plan(ctx context.Context, project model.Project) (Plan, error) {
	return BuildPlan(ctx, s.store, project)
}

// BuildPlan evaluates the preview from the store alone. Everything the preview
// needs is recorded state, so the dashboard can answer "what would happen" with
// no provider process attached.
func BuildPlan(ctx context.Context, db *store.Store, project model.Project) (Plan, error) {
	s := &planSource{store: db}
	plan := Plan{ProjectID: project.ID, ProjectName: project.Name}
	switch project.State {
	case "COMPLETED":
		plan.add("BLOCK", "project state", "목표가 완료되어 더 실행할 작업이 없습니다")
	case "CANCELLED", "BLOCKED", "FAILED":
		plan.add("BLOCK", "project state", fmt.Sprintf("프로젝트 상태가 %s 라 사람이 먼저 해결해야 합니다", project.State))
	case "RUNNING", "VERIFYING", "PREFLIGHT", "DRAINING", "CHECKPOINTING", "RESUMING", "WAITING_QUOTA":
		plan.add("BLOCK", "project state", fmt.Sprintf("이미 %s 상태입니다. 현재 실행이 끝난 뒤 다시 확인하세요", project.State))
	default:
		plan.add("OK", "project state", project.State)
	}
	goal, err := s.store.CurrentGoal(ctx, project.ID)
	if errors.Is(err, store.ErrNotFound) {
		plan.add("BLOCK", "goal", "활성 목표가 없습니다 (goalforge goal set)")
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	plan.GoalTitle = goal.Title
	plan.Gates, err = s.store.ListGates(ctx, project.ID)
	if err != nil {
		return plan, err
	}
	required := 0
	for _, gate := range plan.Gates {
		if gate.Required {
			required++
		}
	}
	switch {
	case len(plan.Gates) == 0:
		plan.add("BLOCK", "gates", "검증 게이트가 없어 실행이 거부됩니다 (goalforge verify template go-api)")
	case required == 0:
		plan.add("BLOCK", "gates", "필수 게이트가 없어 실행이 거부됩니다")
	default:
		plan.add("OK", "gates", fmt.Sprintf("%d개 중 필수 %d개가 실행 후 검증합니다", len(plan.Gates), required))
	}
	if budget, budgetErr := s.store.ProjectBudgetUsage(ctx, project.ID); budgetErr == nil {
		plan.Budget = budget
	} else if !errors.Is(budgetErr, store.ErrNotFound) {
		return plan, budgetErr
	}
	plan.appendBudgetChecks()
	selection, selectErr := s.store.PreviewNextWorkItem(ctx, goal.ID)
	plan.Selection, plan.Skipped = selection, selection.Skipped
	switch {
	case errors.Is(selectErr, store.ErrNotFound):
		plan.add("BLOCK", "work item", "실행할 수 있는 작업이 없습니다 (goalforge ideas 또는 replan)")
		return plan, nil
	case errors.Is(selectErr, store.ErrAllCandidatesConflict):
		plan.add("BLOCK", "work item", fmt.Sprintf("후보 %d건이 모두 진행 중인 작업과 범위가 겹칩니다. 현재 실행이 끝나면 풀립니다", selection.Candidates))
		return plan, nil
	case selectErr != nil && strings.Contains(selectErr.Error(), "WIP limit"):
		plan.add("BLOCK", "work item", selectErr.Error())
		return plan, nil
	case selectErr != nil:
		return plan, selectErr
	}
	item := selection.Chosen
	plan.WorkItem = &item
	plan.SelectionReason = selectionReason(selection)
	plan.add("OK", "work item", fmt.Sprintf("%s — %s", item.ID, item.Title))
	if strings.TrimSpace(item.ChangeScope) == "" {
		plan.add("WARN", "change scope", "변경 범위가 선언되지 않아 범위 이탈을 검사할 수 없고 병렬 실행도 막힙니다")
	} else {
		plan.add("OK", "change scope", item.ChangeScope)
	}
	if strings.TrimSpace(item.Acceptance) == "" {
		plan.add("WARN", "acceptance", "완료 기준이 없어 무엇이 끝인지 실행 세션이 판단하게 됩니다")
	}
	plan.Model, err = s.store.SelectModelForTask(ctx, project.ID, project.Model, project.FallbackModel, string(model.TaskContinueGoal))
	if err != nil {
		return plan, err
	}
	plan.add("OK", "model", fmt.Sprintf("%s — %s", orDefaultModel(plan.Model.Model), plan.Model.Reason))
	plan.Forecast, err = s.store.ForecastTokens(ctx, project.ID, string(model.TaskContinueGoal))
	if err != nil {
		return plan, err
	}
	plan.appendForecastChecks(item)
	plan.Isolated = project.WorktreeEnabled
	plan.Workspace = project.RepositoryPath
	if plan.Isolated {
		plan.Workspace = filepath.Join(project.RepositoryPath+".goalforge-worktrees", item.ID)
		plan.add("OK", "workspace", "격리된 worktree 에서 실행됩니다: "+plan.Workspace)
	} else {
		plan.add("WARN", "workspace", "worktree 가 꺼져 있어 저장소에서 직접 실행됩니다 (goalforge project init --worktrees)")
	}
	if _, err = RefreshEvidence(ctx, s.store, project); err != nil {
		return plan, err
	}
	readiness, err := s.store.ReadinessInput(ctx, project.ID)
	if err != nil {
		return plan, err
	}
	// A contract with requirements nothing can settle, or requirements that
	// contradict each other, does not stop a run — it stops the goal from ever
	// being judged met, which is a different thing and is reported as such.
	if contract, contractErr := db.CurrentContract(ctx, project.ID); contractErr == nil {
		if unconfirmed := contract.Unconfirmed(); len(unconfirmed) > 0 {
			names := make([]string, 0, len(unconfirmed))
			for _, outcome := range unconfirmed {
				names = append(names, outcome.Key)
			}
			plan.add("WARN", "contract", fmt.Sprintf("판정 방법이 없는 필수 결과 %d건 (%s) — 정해지기 전까지 목표는 완료로 판정되지 않습니다",
				len(unconfirmed), strings.Join(names, ", ")))
		}
		for _, conflict := range contract.Conflicts() {
			plan.add("WARN", "contract conflict", fmt.Sprintf("%s vs %s — %s", conflict.Left.Key, conflict.Right.Key, conflict.Detail))
		}
	} else if !errors.Is(contractErr, store.ErrNotFound) {
		return plan, contractErr
	}
	// Readiness findings are warnings here even when doctor calls them
	// blocking: a criterion with no gate does not stop the run, it stops the
	// goal from ever being judged complete. Reporting them as BLOCK would make
	// the preview refuse a run that would in fact proceed.
	for _, check := range diagnostics.CheckReadiness(readiness) {
		if check.Level == diagnostics.LevelOK {
			continue
		}
		detail := check.Detail
		if check.Level == diagnostics.LevelFail {
			detail = "실행은 진행되지만 목표가 완료로 판정될 수 없습니다 — " + detail
		}
		plan.add("WARN", "readiness: "+check.Name, detail)
	}
	return plan, nil
}

// planSource narrows the plan to the store methods it reads, which is what
// makes it safe to say the preview mutates nothing.
type planSource struct{ store *store.Store }

func (p *Plan) appendBudgetChecks() {
	switch {
	case p.Budget.TokenLimit > 0 && p.Budget.TokensUsed >= p.Budget.TokenLimit:
		p.add("BLOCK", "token budget", fmt.Sprintf("토큰 예산 소진: %d / %d", p.Budget.TokensUsed, p.Budget.TokenLimit))
	case p.Budget.TokenLimit > 0:
		p.add("OK", "token budget", fmt.Sprintf("%d / %d 사용 (%.0f%% 남음)", p.Budget.TokensUsed, p.Budget.TokenLimit,
			100-float64(p.Budget.TokensUsed)/float64(p.Budget.TokenLimit)*100))
	default:
		p.add("WARN", "token budget", "토큰 예산이 없어 상한 없이 실행됩니다")
	}
	switch {
	case p.Budget.CostLimitUSD > 0 && p.Budget.CostUsedUSD >= p.Budget.CostLimitUSD:
		p.add("BLOCK", "cost budget", fmt.Sprintf("비용 예산 소진: $%.2f / $%.2f", p.Budget.CostUsedUSD, p.Budget.CostLimitUSD))
	case p.Budget.CostLimitUSD > 0:
		p.add("OK", "cost budget", fmt.Sprintf("$%.2f / $%.2f 사용", p.Budget.CostUsedUSD, p.Budget.CostLimitUSD))
	default:
		p.add("WARN", "cost budget", "비용 예산이 없어 상한 없이 실행됩니다")
	}
}

// appendForecastChecks compares what this run is expected to use against what
// is left, so "this will not fit" is known before the call is made.
func (p *Plan) appendForecastChecks(item model.WorkItem) {
	expected := item.EstimatedTokens
	source := "직접 입력한 예상치"
	if expected == 0 {
		expected = p.Forecast.Expected
		source = fmt.Sprintf("실행 기록 %d건 기반 예측 (신뢰도 %s)", p.Forecast.Samples, p.Forecast.Confidence)
	}
	if expected == 0 {
		p.add("WARN", "forecast", "예상 토큰이 없고 추정할 실행 기록도 없어 비용을 예측할 수 없습니다")
		return
	}
	// Cost per token comes from what this project has actually been charged,
	// so a model or provider change is reflected without hardcoded pricing.
	if p.Budget.TokensUsed > 0 && p.Budget.CostUsedUSD > 0 {
		p.EstimatedCostUSD = p.Budget.CostUsedUSD / float64(p.Budget.TokensUsed) * float64(expected)
	}
	detail := fmt.Sprintf("약 %d 토큰 — %s", expected, source)
	if p.Forecast.Samples > 0 && item.EstimatedTokens == 0 {
		detail += fmt.Sprintf(", 범위 %d~%d", p.Forecast.Low, p.Forecast.High)
	}
	if p.EstimatedCostUSD > 0 {
		detail += fmt.Sprintf(", 약 $%.4f", p.EstimatedCostUSD)
	}
	level := "OK"
	if p.Budget.TokenLimit > 0 && p.Budget.TokensUsed+expected > p.Budget.TokenLimit {
		level = "WARN"
		detail += " — 이 실행으로 토큰 예산을 넘길 수 있습니다"
	}
	if p.Budget.CostLimitUSD > 0 && p.EstimatedCostUSD > 0 && p.Budget.CostUsedUSD+p.EstimatedCostUSD > p.Budget.CostLimitUSD {
		level = "WARN"
		detail += " — 이 실행으로 비용 예산을 넘길 수 있습니다"
	}
	p.add(level, "forecast", detail)
}

// selectionReason explains the choice in the same terms the planner used.
func selectionReason(selection store.Selection) string {
	reason := fmt.Sprintf("실행 가능한 후보 %d건 가운데 우선순위가 가장 높습니다", selection.Candidates)
	if selection.Chosen.Status == "APPROVED" {
		reason = fmt.Sprintf("승인된 작업이라 백로그보다 먼저 선택되었습니다 (후보 %d건)", selection.Candidates)
	}
	if len(selection.Skipped) > 0 {
		reason += fmt.Sprintf(". 더 높은 우선순위 %d건은 범위가 겹쳐 건너뛰었습니다", len(selection.Skipped))
	}
	if selection.HumanHeld > 0 {
		reason += fmt.Sprintf(". 사람이 쥔 작업 %d건은 동시 구현 한도에서 제외됩니다", selection.HumanHeld)
	}
	return reason
}

func orDefaultModel(name string) string {
	if name == "" {
		return "제공자 기본값"
	}
	return name
}

// RefreshEvidence re-checks whether a project's verification evidence still
// describes the code and gates in place now. It is called wherever current
// state is read — status, the preview, the dashboard — because an invalidation
// that depends on every mutation site remembering to declare it is one that
// eventually gets forgotten.
func RefreshEvidence(ctx context.Context, db *store.Store, project model.Project) (int64, error) {
	goal, err := db.CurrentGoal(ctx, project.ID)
	if errors.Is(err, store.ErrNotFound) {
		// A completed goal still has evidence, and code that moves after
		// completion is exactly the case where it stops being true.
		goal, err = db.LatestGoal(ctx, project.ID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	gates, err := db.ListGates(ctx, project.ID)
	if err != nil {
		return 0, err
	}
	// A repository that cannot be inspected yields no tree identity, and the
	// gate comparison still applies: a missing signal must not be read as a
	// match.
	treeID := ""
	if tree, treeErr := gitops.TreeID(ctx, project.RepositoryPath); treeErr == nil {
		treeID = store.WorkspaceTreeID(project.RepositoryPath, tree)
	}
	return db.RefreshEvidence(ctx, project.ID, goal.ID, treeID, gates)
}
