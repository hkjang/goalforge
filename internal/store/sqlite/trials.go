package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/goalforge/goalforge/internal/evaluation"
)

// SaveCaseSpec stores what a case needs in order to be re-executed: the pinned
// fixture, the criteria and gates that judge it, and the limits it runs under.
// A case without these can only have existing runs attached to it, which
// measures those runs rather than the configuration.
func (s *Store) SaveCaseSpec(ctx context.Context, caseID string, spec evaluation.CaseSpec) error {
	criteria, err := json.Marshal(spec.Criteria)
	if err != nil {
		return err
	}
	gates, err := json.Marshal(struct {
		Gates []evaluation.Gate
		Seed  []evaluation.SeedWorkItem
	}{spec.Gates, spec.SeedWork})
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE evaluation_cases SET fixture=?,fixture_ref=?,clean_tree_id=?,criteria_json=?,gates_json=?,token_budget=?,cost_budget_usd=?,timeout_seconds=? WHERE id=?`,
		spec.Fixture, spec.Ref, spec.CleanTreeID, string(criteria), string(gates),
		spec.TokenBudget, spec.CostBudgetUSD, spec.TimeoutSeconds, caseID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

// CaseSpec loads a case as something re-executable.
func (s *Store) CaseSpec(ctx context.Context, projectID, caseID string) (evaluation.CaseSpec, error) {
	var spec evaluation.CaseSpec
	var criteria, gates string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,kind,COALESCE(fixture,''),COALESCE(fixture_ref,''),COALESCE(clean_tree_id,''),goal_title,goal_objective,COALESCE(criteria_json,''),COALESCE(gates_json,''),COALESCE(token_budget,0),COALESCE(cost_budget_usd,0),COALESCE(timeout_seconds,0) FROM evaluation_cases WHERE id=? AND project_id=?`, caseID, projectID).
		Scan(&spec.CaseID, &spec.Name, &spec.Kind, &spec.Fixture, &spec.Ref, &spec.CleanTreeID,
			&spec.GoalTitle, &spec.GoalObjective, &criteria, &gates, &spec.TokenBudget, &spec.CostBudgetUSD, &spec.TimeoutSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return spec, ErrNotFound
	}
	if err != nil {
		return spec, err
	}
	if criteria != "" {
		if err = json.Unmarshal([]byte(criteria), &spec.Criteria); err != nil {
			return spec, err
		}
	}
	if gates != "" {
		var bundle struct {
			Gates []evaluation.Gate
			Seed  []evaluation.SeedWorkItem
		}
		if err = json.Unmarshal([]byte(gates), &bundle); err != nil {
			return spec, err
		}
		spec.Gates, spec.SeedWork = bundle.Gates, bundle.Seed
	}
	return spec, nil
}

// RecordTrial stores one measured execution, including the ones that failed or
// could not be measured.
func (s *Store) RecordTrial(ctx context.Context, trial evaluation.Trial) error {
	completed := 0
	if trial.Completed {
		completed = 1
	}
	created := trial.StartedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_trials(id,case_id,label,repetition,condition_hash,clean_tree_id,status,detail,completed,tokens,cost_usd,interventions,duration_seconds,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		NewID("TRIAL"), trial.CaseID, trial.Label, trial.Repetition, trial.ConditionHash, trial.CleanTreeID,
		trial.Status, trial.Detail, completed, trial.Tokens, trial.CostUSD, trial.Interventions,
		trial.DurationSeconds, created.Format(time.RFC3339Nano))
	return err
}

// TrialSummary aggregates a label's trials. Repetition stability is reported
// separately from the single-run rate because a tool that succeeds once and a
// tool that succeeds every time are not equally trustworthy.
type TrialSummary struct {
	Label, ConditionHash string
	Trials               int
	Passed               int
	Failed               int
	Invalid              int
	Errored              int
	PassRate             float64
	// StableCases is the share of cases where every valid repetition passed.
	StableCases          float64
	CasesRun             int
	AverageCostUSD       float64
	AverageTokens        int64
	AverageSeconds       float64
	TotalCostUSD         float64
	CostPerSuccessUSD    float64
	AverageInterventions float64
}

// CompareTrials summarizes each label's trials for a project, optionally for
// one case. Invalid trials are excluded from the rate and reported separately:
// a trial that could not be measured says nothing about the configuration, and
// counting it either way misreports the suite.
func (s *Store) CompareTrials(ctx context.Context, projectID, caseID string) ([]TrialSummary, error) {
	query := `SELECT t.label,t.condition_hash,t.case_id,t.status,t.tokens,t.cost_usd,t.interventions,t.duration_seconds
FROM evaluation_trials t JOIN evaluation_cases c ON c.id=t.case_id WHERE c.project_id=?`
	args := []any{projectID}
	if caseID != "" {
		query += ` AND t.case_id=?`
		args = append(args, caseID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type accumulator struct {
		summary   TrialSummary
		caseTotal map[string]int
		casePass  map[string]int
	}
	grouped := map[string]*accumulator{}
	for rows.Next() {
		var label, condition, trialCase, status string
		var tokens int64
		var cost, duration float64
		var interventions int
		if err = rows.Scan(&label, &condition, &trialCase, &status, &tokens, &cost, &interventions, &duration); err != nil {
			return nil, err
		}
		entry, ok := grouped[label]
		if !ok {
			entry = &accumulator{summary: TrialSummary{Label: label, ConditionHash: condition},
				caseTotal: map[string]int{}, casePass: map[string]int{}}
			grouped[label] = entry
		}
		entry.summary.Trials++
		entry.summary.TotalCostUSD += cost
		entry.summary.AverageTokens += tokens
		entry.summary.AverageSeconds += duration
		entry.summary.AverageInterventions += float64(interventions)
		switch status {
		case evaluation.StatusPassed:
			entry.summary.Passed++
			entry.caseTotal[trialCase]++
			entry.casePass[trialCase]++
		case evaluation.StatusInvalid:
			entry.summary.Invalid++
		case evaluation.StatusError:
			entry.summary.Errored++
		default:
			entry.summary.Failed++
			entry.caseTotal[trialCase]++
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := make([]TrialSummary, 0, len(grouped))
	for _, entry := range grouped {
		summary := entry.summary
		measured := summary.Passed + summary.Failed
		if measured > 0 {
			summary.PassRate = float64(summary.Passed) / float64(measured) * 100
		}
		if summary.Trials > 0 {
			summary.AverageTokens /= int64(summary.Trials)
			summary.AverageSeconds /= float64(summary.Trials)
			summary.AverageInterventions /= float64(summary.Trials)
			summary.AverageCostUSD = summary.TotalCostUSD / float64(summary.Trials)
		}
		if summary.Passed > 0 {
			summary.CostPerSuccessUSD = summary.TotalCostUSD / float64(summary.Passed)
		}
		stable := 0
		for caseKey, total := range entry.caseTotal {
			if entry.casePass[caseKey] == total {
				stable++
			}
		}
		summary.CasesRun = len(entry.caseTotal)
		if summary.CasesRun > 0 {
			summary.StableCases = float64(stable) / float64(summary.CasesRun) * 100
		}
		result = append(result, summary)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PassRate != result[j].PassRate {
			return result[i].PassRate > result[j].PassRate
		}
		return result[i].CostPerSuccessUSD < result[j].CostPerSuccessUSD
	})
	return result, nil
}
