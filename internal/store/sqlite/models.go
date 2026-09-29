package sqlite

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ModelStat is one model's record on one kind of task for a project.
type ModelStat struct {
	Model, TaskType   string
	Runs, Verified    int64
	Tokens            int64
	CostUSD           float64
	AverageSeconds    float64
	SuccessRate       float64
	AverageCostPerRun float64
}

// ModelStats summarizes how each model has actually performed on a task type,
// measured by verification outcomes rather than by whether the provider call
// returned. A run that finished and failed its gates is not a success.
func (s *Store) ModelStats(ctx context.Context, projectID, taskType string) ([]ModelStat, error) {
	// Usage is summed per run *before* the join, so each run contributes
	// exactly one row here.
	//
	// Joining the ledger directly counted every per-run fact once per usage
	// row: a provider reports input, output, and cost separately, so a single
	// successful run with three rows scored three successes against one run
	// and the rate came out at 300%. The duration average was weighted the
	// same way — a run that wrote more accounting counted as several runs.
	query := `SELECT COALESCE(NULLIF(r.model,''),'default'),
COUNT(*),
COALESCE(SUM(CASE WHEN r.state IN ('CHECKPOINTING','COMPLETED') THEN 1 ELSE 0 END),0),
COALESCE(SUM(COALESCE(u.tokens,0)),0),
COALESCE(SUM(COALESCE(u.cost,0)),0),
COALESCE(AVG(CASE WHEN r.ended_at IS NOT NULL THEN (julianday(r.ended_at)-julianday(r.started_at))*86400 END),0)
FROM runs r
LEFT JOIN (
 SELECT run_id,
  SUM(CASE WHEN token_type<>'cost_usd' THEN amount ELSE 0 END) AS tokens,
  SUM(cost) AS cost
 FROM usage_ledger GROUP BY run_id
) u ON u.run_id=r.id
WHERE r.project_id=?`
	args := []any{projectID}
	if taskType != "" {
		query += ` AND r.task_type=?`
		args = append(args, taskType)
	}
	query += ` GROUP BY COALESCE(NULLIF(r.model,''),'default')`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ModelStat
	for rows.Next() {
		stat := ModelStat{TaskType: taskType}
		if err = rows.Scan(&stat.Model, &stat.Runs, &stat.Verified, &stat.Tokens, &stat.CostUSD, &stat.AverageSeconds); err != nil {
			return nil, err
		}
		if stat.Runs > 0 {
			stat.SuccessRate = float64(stat.Verified) / float64(stat.Runs) * 100
			stat.AverageCostPerRun = stat.CostUSD / float64(stat.Runs)
		}
		result = append(result, stat)
	}
	return result, rows.Err()
}

// ModelChoice is a model selection with the reason it was made, so an
// automatic change of model is explainable rather than surprising.
type ModelChoice struct {
	Model, Reason, Source string
	Considered            []ModelStat
}

// minRunsForEvidence is how many runs a model needs before its record is
// allowed to override the configured model. Below it, one unlucky run would
// flip the choice.
const minRunsForEvidence = 3

// SelectModelForTask chooses among the models the project has approved — its
// configured model and its approved fallback — using the verified success rate
// first and the cost per run as the tie-break. The configured model is kept
// unless the evidence is strong enough to justify moving, and the reason is
// always recorded.
func (s *Store) SelectModelForTask(ctx context.Context, projectID, configured, fallback, taskType string) (ModelChoice, error) {
	choice := ModelChoice{Model: configured, Source: "configured", Reason: "프로젝트에 설정된 모델입니다"}
	if configured == "" {
		choice.Model, choice.Reason = "", "모델이 설정되지 않아 제공자 기본값을 사용합니다"
	}
	allowed := map[string]bool{}
	if configured != "" {
		allowed[configured] = true
	}
	if fallback != "" && fallback != configured {
		allowed[fallback] = true
	}
	if len(allowed) < 2 {
		return choice, nil
	}
	stats, err := s.ModelStats(ctx, projectID, taskType)
	if err != nil {
		return choice, err
	}
	var candidates []ModelStat
	for _, stat := range stats {
		if allowed[stat.Model] && stat.Runs >= minRunsForEvidence {
			candidates = append(candidates, stat)
		}
	}
	choice.Considered = candidates
	if len(candidates) < 2 {
		choice.Reason = fmt.Sprintf("%s 작업에 대해 비교할 만한 실행 기록이 부족해 설정된 모델을 유지합니다", taskTypeLabel(taskType))
		return choice, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SuccessRate != candidates[j].SuccessRate {
			return candidates[i].SuccessRate > candidates[j].SuccessRate
		}
		return candidates[i].AverageCostPerRun < candidates[j].AverageCostPerRun
	})
	best := candidates[0]
	if best.Model == choice.Model {
		choice.Reason = fmt.Sprintf("%s 작업에서 검증 통과율 %.0f%% 로 가장 좋아 설정된 모델을 유지합니다", taskTypeLabel(taskType), best.SuccessRate)
		return choice, nil
	}
	current := ModelStat{Model: choice.Model}
	for _, stat := range candidates {
		if stat.Model == choice.Model {
			current = stat
		}
	}
	choice.Model, choice.Source = best.Model, "history"
	choice.Reason = fmt.Sprintf("%s 작업에서 %s 의 검증 통과율 %.0f%% (평균 $%.4f) 가 %s 의 %.0f%% (평균 $%.4f) 보다 높습니다",
		taskTypeLabel(taskType), best.Model, best.SuccessRate, best.AverageCostPerRun, current.Model, current.SuccessRate, current.AverageCostPerRun)
	return choice, nil
}

func taskTypeLabel(taskType string) string {
	if strings.TrimSpace(taskType) == "" {
		return "전체"
	}
	return taskType
}
