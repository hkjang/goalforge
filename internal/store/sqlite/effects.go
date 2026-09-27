package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// External effect kinds. These are the actions whose result lives outside
// GoalForge's database, so a crash between doing one and recording it leaves
// the two disagreeing.
const (
	EffectPublishBranch = "PUBLISH_BRANCH"
	EffectMergeBranch   = "MERGE_BRANCH"
)

// External effect states.
const (
	// EffectIntended is written before the action is attempted, so a crash
	// leaves a record that something may have happened.
	EffectIntended  = "INTENDED"
	EffectSucceeded = "SUCCEEDED"
	EffectFailed    = "FAILED"
	// EffectUnknown means the attempt neither clearly succeeded nor clearly
	// failed. It is the state a retry must not ignore.
	EffectUnknown = "UNKNOWN"
)

// ExternalEffect records an action whose result is outside this database.
// Retries are expected, so the ledger exists to answer "did this already
// happen" before doing it again — there is no globally exactly-once execution
// to rely on.
type ExternalEffect struct {
	ID, ProjectID, RunID, WorkItemID string
	Kind, Key                        string
	// Target is where the effect lands (a remote, or a branch to merge into)
	// and Branch is what is being sent. RequestHash is the commit the intent
	// was formed against, which is what a reconciliation compares.
	Target, Branch, RequestHash string
	State, Result               string
	Attempts                    int
	CreatedAt, UpdatedAt        time.Time
}

// Pending reports whether this effect's outcome is still unresolved, which is
// the case a retry has to reconcile rather than repeat.
func (e ExternalEffect) Pending() bool {
	return e.State == EffectIntended || e.State == EffectUnknown
}

// EffectKey identifies one intended change so the same intent maps to the same
// ledger row across retries and processes.
func EffectKey(kind, projectID, workItemID, target, commitSHA string) string {
	payload := strings.Join([]string{kind, projectID, workItemID, target, commitSHA}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))[:24]
}

// BeginEffect claims the intent to perform an external action. It returns the
// existing record when one is present: a completed effect must not be repeated,
// and an unresolved one must be reconciled before anything else is attempted.
func (s *Store) BeginEffect(ctx context.Context, effect ExternalEffect) (ExternalEffect, bool, error) {
	if effect.ProjectID == "" || effect.Kind == "" || effect.Key == "" {
		return effect, false, errors.New("project, kind, and effect key are required")
	}
	existing, err := s.EffectByKey(ctx, effect.Key)
	switch {
	case err == nil:
		return existing, false, nil
	case !errors.Is(err, ErrNotFound):
		return effect, false, err
	}
	effect.ID = NewID("EFF")
	effect.State = EffectIntended
	effect.Attempts = 1
	effect.CreatedAt = time.Now().UTC()
	effect.UpdatedAt = effect.CreatedAt
	_, err = s.db.ExecContext(ctx, `INSERT INTO external_effects(id,project_id,run_id,work_item_id,kind,effect_key,target,branch,request_hash,state,result,attempts,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'',?,?,?)`,
		effect.ID, effect.ProjectID, effect.RunID, effect.WorkItemID, effect.Kind, effect.Key, effect.Target,
		effect.Branch, effect.RequestHash, effect.State, effect.Attempts, effect.CreatedAt.Format(time.RFC3339Nano), effect.UpdatedAt.Format(time.RFC3339Nano))
	return effect, true, err
}

// SettleEffect records how an attempt ended. An error whose outcome could not
// be determined is stored as UNKNOWN rather than FAILED: retrying a failure is
// safe, retrying something that may have succeeded is not.
func (s *Store) SettleEffect(ctx context.Context, key, state, result string) error {
	switch state {
	case EffectSucceeded, EffectFailed, EffectUnknown:
	default:
		return fmt.Errorf("invalid effect state %q", state)
	}
	outcome, err := s.db.ExecContext(ctx, `UPDATE external_effects SET state=?,result=?,updated_at=? WHERE effect_key=?`,
		state, result, time.Now().UTC().Format(time.RFC3339Nano), key)
	if err != nil {
		return err
	}
	if n, _ := outcome.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

// RetryEffect reopens an effect for another attempt, counting it. A failed or
// unresolved-but-now-settled effect is safe to retry; a succeeded one is not,
// which is why that state is excluded here rather than left to the caller.
func (s *Store) RetryEffect(ctx context.Context, key string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE external_effects SET state=?,attempts=attempts+1,updated_at=? WHERE effect_key=? AND state<>?`,
		EffectIntended, time.Now().UTC().Format(time.RFC3339Nano), key, EffectSucceeded)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("effect %s cannot be retried in its current state", key)
	}
	return nil
}

func (s *Store) EffectByKey(ctx context.Context, key string) (ExternalEffect, error) {
	var effect ExternalEffect
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,run_id,work_item_id,kind,effect_key,target,COALESCE(branch,''),request_hash,state,result,attempts,created_at,updated_at FROM external_effects WHERE effect_key=?`, key).
		Scan(&effect.ID, &effect.ProjectID, &effect.RunID, &effect.WorkItemID, &effect.Kind, &effect.Key,
			&effect.Target, &effect.Branch, &effect.RequestHash, &effect.State, &effect.Result, &effect.Attempts, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return effect, ErrNotFound
	}
	if err == nil {
		effect.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		effect.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	}
	return effect, err
}

// UnresolvedEffects lists effects whose outcome is not known, which is what a
// worker has to settle before doing anything that could repeat them.
func (s *Store) UnresolvedEffects(ctx context.Context, projectID string) ([]ExternalEffect, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,run_id,work_item_id,kind,effect_key,target,COALESCE(branch,''),request_hash,state,result,attempts,created_at,updated_at FROM external_effects WHERE project_id=? AND state IN (?,?) ORDER BY created_at`,
		projectID, EffectIntended, EffectUnknown)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ExternalEffect
	for rows.Next() {
		var effect ExternalEffect
		var created, updated string
		if err = rows.Scan(&effect.ID, &effect.ProjectID, &effect.RunID, &effect.WorkItemID, &effect.Kind, &effect.Key,
			&effect.Target, &effect.Branch, &effect.RequestHash, &effect.State, &effect.Result, &effect.Attempts, &created, &updated); err != nil {
			return nil, err
		}
		effect.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		effect.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		result = append(result, effect)
	}
	return result, rows.Err()
}

// ListEffects returns a project's effect ledger, newest first.
func (s *Store) ListEffects(ctx context.Context, projectID string, limit int) ([]ExternalEffect, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,run_id,work_item_id,kind,effect_key,target,COALESCE(branch,''),request_hash,state,result,attempts,created_at,updated_at FROM external_effects WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ExternalEffect
	for rows.Next() {
		var effect ExternalEffect
		var created, updated string
		if err = rows.Scan(&effect.ID, &effect.ProjectID, &effect.RunID, &effect.WorkItemID, &effect.Kind, &effect.Key,
			&effect.Target, &effect.Branch, &effect.RequestHash, &effect.State, &effect.Result, &effect.Attempts, &created, &updated); err != nil {
			return nil, err
		}
		effect.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		effect.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		result = append(result, effect)
	}
	return result, rows.Err()
}
