package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/rrsi"
)

// SaveSelectionPolicy records how strict one project is about calling a
// configuration change an improvement.
//
// It is per project because both numbers that matter are per project: how much
// an unchanged configuration moves depends on that project's evaluation cases,
// and how much extra spend a gain is worth depends on what the project is for.
func (s *Store) SaveSelectionPolicy(ctx context.Context, projectID string, policy rrsi.Policy) error {
	if projectID == "" {
		return errors.New("project is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO selection_policies(project_id,noise_band,calibrated_from,beta0,beta1,weight_score,weight_cost,weight_novelty,min_trials,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(project_id) DO UPDATE SET noise_band=excluded.noise_band,calibrated_from=excluded.calibrated_from,
beta0=excluded.beta0,beta1=excluded.beta1,weight_score=excluded.weight_score,weight_cost=excluded.weight_cost,
weight_novelty=excluded.weight_novelty,min_trials=excluded.min_trials,updated_at=excluded.updated_at`,
		projectID, policy.NoiseBand, policy.CalibratedFrom, policy.Beta0, policy.Beta1,
		policy.WeightScore, policy.WeightCost, policy.WeightNovelty, policy.MinTrials,
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// SelectionPolicyFor reads a project's policy.
//
// A project with none gets the default, which has no noise band — so nothing
// can be judged until somebody measures one. The default has to be the one
// that refuses: a missing policy read as "use the usual band" would apply
// another project's variance to this one's numbers.
func (s *Store) SelectionPolicyFor(ctx context.Context, projectID string) (rrsi.Policy, error) {
	policy := rrsi.DefaultPolicy()
	err := s.db.QueryRowContext(ctx, `SELECT noise_band,calibrated_from,beta0,beta1,weight_score,weight_cost,weight_novelty,min_trials
FROM selection_policies WHERE project_id=?`, projectID).
		Scan(&policy.NoiseBand, &policy.CalibratedFrom, &policy.Beta0, &policy.Beta1,
			&policy.WeightScore, &policy.WeightCost, &policy.WeightNovelty, &policy.MinTrials)
	if errors.Is(err, sql.ErrNoRows) {
		return rrsi.DefaultPolicy(), nil
	}
	return policy, err
}

// CalibrationTrials collects the repeated outcomes of one configuration's
// evaluation, which is what a noise band is measured from.
//
// It reads one condition hash on purpose. Pooling repetitions across
// configurations would measure how much the configurations differ, which is
// the thing the band is supposed to be compared against — the measurement
// would contain its own answer.
func (s *Store) CalibrationTrials(ctx context.Context, conditionHash string) ([]rrsi.CaseTrials, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT case_id,status FROM evaluation_trials
WHERE condition_hash=? AND status IN ('PASSED','FAILED') ORDER BY case_id,repetition,id`, conditionHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	order := []string{}
	byCase := map[string][]bool{}
	for rows.Next() {
		var caseID, status string
		if err = rows.Scan(&caseID, &status); err != nil {
			return nil, err
		}
		if _, seen := byCase[caseID]; !seen {
			order = append(order, caseID)
		}
		byCase[caseID] = append(byCase[caseID], status == "PASSED")
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	trials := make([]rrsi.CaseTrials, 0, len(order))
	for _, caseID := range order {
		trials = append(trials, rrsi.CaseTrials{CaseID: caseID, Outcomes: byCase[caseID]})
	}
	return trials, nil
}

// RecordProposal stores one candidate's edits and what measuring it
// established.
//
// The record is written to be read: it is what stops the search re-drawing an
// explanation it has already falsified. A history nobody consults is the same
// as no history, and the run spends its rounds rediscovering what it knew.
func (s *Store) RecordProposal(ctx context.Context, projectID string, record rrsi.Record) error {
	if projectID == "" || len(record.Edits) == 0 {
		return errors.New("project and at least one edit are required")
	}
	edits, err := json.Marshal(record.Edits)
	if err != nil {
		return err
	}
	accepted := 0
	if record.Accepted {
		accepted = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO config_proposals(id,project_id,round,label,edits,verdict,score_delta,cost_delta,score,accepted,screen_refusal,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		NewID("PRP"), projectID, record.Round, record.Label, string(edits), record.Verdict,
		record.ScoreDelta, record.CostDelta, record.Score, accepted, record.ScreenRefusal,
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// ProposalHistory returns a project's proposals oldest first.
//
// Oldest first because the order is the evidence: a run of failures ending in
// a success means something different from a success followed by failures, and
// the falsified set is built by walking forward.
func (s *Store) ProposalHistory(ctx context.Context, projectID string) (rrsi.History, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT round,label,edits,verdict,score_delta,cost_delta,score,accepted,screen_refusal
FROM config_proposals WHERE project_id=? ORDER BY round,created_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var history rrsi.History
	for rows.Next() {
		var record rrsi.Record
		var edits string
		var accepted int
		if err = rows.Scan(&record.Round, &record.Label, &edits, &record.Verdict, &record.ScoreDelta,
			&record.CostDelta, &record.Score, &accepted, &record.ScreenRefusal); err != nil {
			return nil, err
		}
		record.Accepted = accepted == 1
		if err = json.Unmarshal([]byte(edits), &record.Edits); err != nil {
			return nil, err
		}
		history = append(history, record)
	}
	return history, rows.Err()
}

// ScoreTrajectory is the accepted score after each round, which is what a
// stall is measured over.
//
// Only accepted proposals move it. A round where every candidate was refused
// left the incumbent where it was, and recording the best refused candidate's
// score would show movement that never happened.
func (s *Store) ScoreTrajectory(ctx context.Context, projectID string) ([]float64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT score FROM config_proposals
WHERE project_id=? AND accepted=1 ORDER BY round,created_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var trajectory []float64
	for rows.Next() {
		var score float64
		if err = rows.Scan(&score); err != nil {
			return nil, err
		}
		trajectory = append(trajectory, score)
	}
	return trajectory, rows.Err()
}

// EvaluationCaseNames is what the leakage screen compares a change against.
func (s *Store) EvaluationCaseNames(ctx context.Context, projectID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM evaluation_cases WHERE project_id=? ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
