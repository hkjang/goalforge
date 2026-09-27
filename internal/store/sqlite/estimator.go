package sqlite

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// EstimateWorkItemTokens conservatively predicts the tokens the next
// work-item run will need when no manual estimate exists: the average total
// of the most recent recorded work runs with a 50% safety margin (section
// 6.3: judge conservatively from recent average usage). Returns ErrNotFound
// when the project has no recorded work-run usage yet.
func (s *Store) EstimateWorkItemTokens(ctx context.Context, projectID string) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(SUM(l.amount),0) AS total FROM runs r JOIN usage_ledger l ON l.run_id=r.id AND l.token_type<>'cost_usd' WHERE r.project_id=? AND r.work_item_id IS NOT NULL GROUP BY r.id HAVING total>0 ORDER BY MAX(r.started_at) DESC LIMIT 10`, projectID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var sum, count int64
	for rows.Next() {
		var total int64
		if err = rows.Scan(&total); err != nil {
			return 0, err
		}
		sum += total
		count++
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, ErrNotFound
	}
	average := sum / count
	return average + average/2, nil
}

// TokenForecast predicts a run's token use as a range with a stated
// confidence. A single expected number reads as certainty the history does not
// support, and hides that two samples and twenty samples are different things.
type TokenForecast struct {
	TaskType            string
	Expected, Low, High int64
	Samples             int
	// Confidence is none, low, medium, or high, derived from the sample count.
	Confidence string
	Basis      string
}

// ForecastTokens builds the forecast from recent runs of the same task type,
// falling back to every task type when there is not enough history for one.
func (s *Store) ForecastTokens(ctx context.Context, projectID, taskType string) (TokenForecast, error) {
	forecast := TokenForecast{TaskType: taskType, Confidence: "none", Basis: "실행 기록이 없어 예측할 수 없습니다"}
	totals, err := s.runTokenTotals(ctx, projectID, taskType)
	if err != nil {
		return forecast, err
	}
	if len(totals) < 3 && taskType != "" {
		broader, broaderErr := s.runTokenTotals(ctx, projectID, "")
		if broaderErr != nil {
			return forecast, broaderErr
		}
		if len(broader) > len(totals) {
			totals = broader
			forecast.Basis = "해당 작업 유형의 기록이 적어 전체 실행 기록으로 예측했습니다"
		}
	}
	if len(totals) == 0 {
		return forecast, nil
	}
	sort.Slice(totals, func(i, j int) bool { return totals[i] < totals[j] })
	forecast.Samples = len(totals)
	forecast.Expected = totals[len(totals)/2]
	forecast.Low = totals[0]
	forecast.High = totals[len(totals)-1]
	if len(totals) >= 5 {
		forecast.Low = totals[len(totals)/10]
		forecast.High = totals[len(totals)*9/10]
	}
	switch {
	case forecast.Samples >= 10:
		forecast.Confidence = "high"
	case forecast.Samples >= 5:
		forecast.Confidence = "medium"
	default:
		forecast.Confidence = "low"
	}
	if forecast.Basis == "" || forecast.Samples > 0 && forecast.Basis == "실행 기록이 없어 예측할 수 없습니다" {
		forecast.Basis = fmt.Sprintf("최근 실행 %d건의 중앙값과 범위입니다", forecast.Samples)
	}
	return forecast, nil
}

func (s *Store) runTokenTotals(ctx context.Context, projectID, taskType string) ([]int64, error) {
	query := `SELECT COALESCE(SUM(l.amount),0) AS total FROM runs r JOIN usage_ledger l ON l.run_id=r.id AND l.token_type<>'cost_usd' WHERE r.project_id=? AND r.work_item_id IS NOT NULL`
	args := []any{projectID}
	if taskType != "" {
		query += ` AND r.task_type=?`
		args = append(args, taskType)
	}
	query += ` GROUP BY r.id HAVING total>0 ORDER BY MAX(r.started_at) DESC LIMIT 20`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var totals []int64
	for rows.Next() {
		var total int64
		if err = rows.Scan(&total); err != nil {
			return nil, err
		}
		totals = append(totals, total)
	}
	return totals, rows.Err()
}

// EstimateAccuracy compares what work items were estimated to cost against
// what their runs actually used. Tracking the error is what makes a forecast
// improvable instead of merely confident.
type EstimateAccuracy struct {
	Samples             int
	MeanAbsolutePercent float64
	Overestimates       int
	Underestimates      int
}

func (s *Store) EstimateAccuracy(ctx context.Context, projectID string) (EstimateAccuracy, error) {
	var accuracy EstimateAccuracy
	rows, err := s.db.QueryContext(ctx, `SELECT w.estimated_tokens,COALESCE(SUM(l.amount),0) AS actual
FROM runs r JOIN work_items w ON w.id=r.work_item_id
JOIN usage_ledger l ON l.run_id=r.id AND l.token_type<>'cost_usd'
WHERE r.project_id=? AND w.estimated_tokens>0 GROUP BY r.id HAVING actual>0 ORDER BY r.started_at DESC LIMIT 50`, projectID)
	if err != nil {
		return accuracy, err
	}
	defer rows.Close()
	var totalError float64
	for rows.Next() {
		var estimated, actual int64
		if err = rows.Scan(&estimated, &actual); err != nil {
			return accuracy, err
		}
		accuracy.Samples++
		totalError += math.Abs(float64(actual-estimated)) / float64(estimated) * 100
		if estimated > actual {
			accuracy.Overestimates++
		} else if estimated < actual {
			accuracy.Underestimates++
		}
	}
	if err = rows.Err(); err != nil {
		return accuracy, err
	}
	if accuracy.Samples > 0 {
		accuracy.MeanAbsolutePercent = totalError / float64(accuracy.Samples)
	}
	return accuracy, nil
}
