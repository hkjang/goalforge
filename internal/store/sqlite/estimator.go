package sqlite

import (
	"context"
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
