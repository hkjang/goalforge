package sqlite

import (
	"context"
	"database/sql"
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
