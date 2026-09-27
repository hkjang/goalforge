package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// EvaluationCase is a fixed task used to compare configurations against each
// other. Comparing "before and after" on whatever work happened to come up
// measures the work, not the change; a fixed set is what makes a prompt, model,
// or policy change assessable.
type EvaluationCase struct {
	ID, ProjectID, Name string
	// Kind is bug_fix, feature, refactor, or docs.
	Kind, Repository         string
	GoalTitle, GoalObjective string
	Notes                    string
	CreatedAt                time.Time
}

// EvaluationResult is one case executed under one labelled configuration.
type EvaluationResult struct {
	ID, CaseID, Label, RunID, ProjectID string
	Provider, Model, ConfigVersion      string
	Passed                              bool
	Tokens                              int64
	CostUSD                             float64
	Interventions                       int
	DurationSeconds                     float64
	CreatedAt                           time.Time
}

// EvaluationSummary aggregates a label's results for one case or across cases.
type EvaluationSummary struct {
	Label                string
	Runs, Passed         int
	PassRate             float64
	AverageCostUSD       float64
	AverageTokens        int64
	AverageInterventions float64
	AverageSeconds       float64
}

func (s *Store) AddEvaluationCase(ctx context.Context, evaluation EvaluationCase) (EvaluationCase, error) {
	if evaluation.ProjectID == "" || strings.TrimSpace(evaluation.Name) == "" || strings.TrimSpace(evaluation.Kind) == "" {
		return evaluation, errors.New("project, name, and kind are required")
	}
	switch evaluation.Kind {
	case "bug_fix", "feature", "refactor", "docs":
	default:
		return evaluation, fmt.Errorf("kind must be bug_fix, feature, refactor, or docs, not %q", evaluation.Kind)
	}
	if evaluation.ID == "" {
		evaluation.ID = NewID("EVAL")
	}
	if evaluation.CreatedAt.IsZero() {
		evaluation.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_cases(id,project_id,name,kind,repository,goal_title,goal_objective,notes,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		evaluation.ID, evaluation.ProjectID, evaluation.Name, evaluation.Kind, evaluation.Repository,
		evaluation.GoalTitle, evaluation.GoalObjective, evaluation.Notes, evaluation.CreatedAt.Format(time.RFC3339Nano))
	return evaluation, err
}

func (s *Store) ListEvaluationCases(ctx context.Context, projectID string) ([]EvaluationCase, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,name,kind,repository,goal_title,goal_objective,notes,created_at FROM evaluation_cases WHERE project_id=? ORDER BY kind,name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EvaluationCase
	for rows.Next() {
		var evaluation EvaluationCase
		var created string
		if err = rows.Scan(&evaluation.ID, &evaluation.ProjectID, &evaluation.Name, &evaluation.Kind, &evaluation.Repository,
			&evaluation.GoalTitle, &evaluation.GoalObjective, &evaluation.Notes, &created); err != nil {
			return nil, err
		}
		evaluation.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, evaluation)
	}
	return result, rows.Err()
}

// RecordEvaluationResult attaches a completed run to a case under a label,
// deriving the metrics from what the run actually did rather than from what
// the caller claims.
func (s *Store) RecordEvaluationResult(ctx context.Context, caseID, label, runID string) (EvaluationResult, error) {
	result := EvaluationResult{ID: NewID("EVR"), CaseID: caseID, Label: label, RunID: runID, CreatedAt: time.Now().UTC()}
	if caseID == "" || label == "" || runID == "" {
		return result, errors.New("case, label, and run are required")
	}
	var started, ended string
	err := s.db.QueryRowContext(ctx, `SELECT project_id,provider,COALESCE(model,''),COALESCE(config_version,''),state,started_at,COALESCE(ended_at,'') FROM runs WHERE id=?`, runID).
		Scan(&result.ProjectID, &result.Provider, &result.Model, &result.ConfigVersion, new(string), &started, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if startedAt, parseErr := time.Parse(time.RFC3339Nano, started); parseErr == nil {
		if endedAt, endErr := time.Parse(time.RFC3339Nano, ended); endErr == nil {
			result.DurationSeconds = endedAt.Sub(startedAt).Seconds()
		}
	}
	// Passing means every required gate passed, which is the only definition
	// that cannot be satisfied by the run merely finishing.
	var failed int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM verification_results WHERE run_id=? AND required=1 AND status<>'PASSED'`, runID).Scan(&failed); err != nil {
		return result, err
	}
	var total int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM verification_results WHERE run_id=? AND required=1`, runID).Scan(&total); err != nil {
		return result, err
	}
	result.Passed = total > 0 && failed == 0
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN token_type<>'cost_usd' THEN amount ELSE 0 END),0),COALESCE(SUM(cost),0) FROM usage_ledger WHERE run_id=?`, runID).
		Scan(&result.Tokens, &result.CostUSD); err != nil {
		return result, err
	}
	// Manual interventions are the approvals and takeovers the work item
	// needed: the cost of a configuration is not only its tokens.
	var workItemID string
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(work_item_id,'') FROM runs WHERE id=?`, runID).Scan(&workItemID); err != nil {
		return result, err
	}
	if workItemID != "" {
		if err = s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM approvals WHERE project_id=? AND work_item_id=?)+(SELECT COUNT(*) FROM takeovers WHERE project_id=? AND work_item_id=?)`,
			result.ProjectID, workItemID, result.ProjectID, workItemID).Scan(&result.Interventions); err != nil {
			return result, err
		}
	}
	passed := 0
	if result.Passed {
		passed = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO evaluation_results(id,case_id,label,run_id,project_id,provider,model,config_version,passed,tokens,cost_usd,interventions,duration_seconds,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		result.ID, result.CaseID, result.Label, result.RunID, result.ProjectID, result.Provider, result.Model,
		result.ConfigVersion, passed, result.Tokens, result.CostUSD, result.Interventions, result.DurationSeconds,
		result.CreatedAt.Format(time.RFC3339Nano))
	return result, err
}

// CompareEvaluations summarizes each label's results, optionally for one case.
func (s *Store) CompareEvaluations(ctx context.Context, projectID, caseID string) ([]EvaluationSummary, error) {
	query := `SELECT r.label,COUNT(*),COALESCE(SUM(r.passed),0),COALESCE(AVG(r.cost_usd),0),COALESCE(AVG(r.tokens),0),COALESCE(AVG(r.interventions),0),COALESCE(AVG(r.duration_seconds),0)
FROM evaluation_results r JOIN evaluation_cases c ON c.id=r.case_id WHERE c.project_id=?`
	args := []any{projectID}
	if caseID != "" {
		query += ` AND r.case_id=?`
		args = append(args, caseID)
	}
	query += ` GROUP BY r.label`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EvaluationSummary
	for rows.Next() {
		var summary EvaluationSummary
		var averageTokens float64
		if err = rows.Scan(&summary.Label, &summary.Runs, &summary.Passed, &summary.AverageCostUSD, &averageTokens,
			&summary.AverageInterventions, &summary.AverageSeconds); err != nil {
			return nil, err
		}
		summary.AverageTokens = int64(averageTokens)
		if summary.Runs > 0 {
			summary.PassRate = float64(summary.Passed) / float64(summary.Runs) * 100
		}
		result = append(result, summary)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PassRate != result[j].PassRate {
			return result[i].PassRate > result[j].PassRate
		}
		return result[i].AverageCostUSD < result[j].AverageCostUSD
	})
	return result, nil
}

// ConfigFingerprint identifies the configuration a run executed under, so a
// later change in success rate or cost can be attributed to a configuration
// change rather than guessed at.
func ConfigFingerprint(provider, modelName string, wipLimit int, repairPolicy RepairPolicy, gates []GateConfig) string {
	payload := struct {
		Provider, Model string
		WIPLimit        int
		Repair          RepairPolicy
		Gates           []GateConfig
	}{provider, modelName, wipLimit, repairPolicy, gates}
	sort.Slice(payload.Gates, func(i, j int) bool { return payload.Gates[i].Type < payload.Gates[j].Type })
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))[:16]
}
