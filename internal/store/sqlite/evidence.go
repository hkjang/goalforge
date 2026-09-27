package sqlite

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// Evidence staleness reasons.
const (
	StaleCriteriaChanged = "goal criteria changed"
	StaleGateChanged     = "verification gate changed"
	StaleCodeChanged     = "verified code changed"
)

// InvalidateEvidence marks a goal's verification results as needing
// re-verification. A passing result is a statement about a particular tree
// checked by a particular command; when either changes the result stops being
// evidence for the current state, and treating it as current is how a goal
// gets reported complete on a check that no longer applies.
func (s *Store) InvalidateEvidence(ctx context.Context, goalID, reason string, checkTypes ...string) (int64, error) {
	query := `UPDATE verification_results SET stale=1,stale_reason=? WHERE goal_id=? AND stale=0 AND status='PASSED'`
	args := []any{reason, goalID}
	if len(checkTypes) > 0 {
		placeholders := ""
		for i, checkType := range checkTypes {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, checkType)
		}
		query += ` AND check_type IN (` + placeholders + `)`
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// InvalidateProjectEvidence invalidates the active goal's evidence, used when
// the repository itself moves under a project.
func (s *Store) InvalidateProjectEvidence(ctx context.Context, projectID, reason string) (int64, error) {
	goal, err := s.CurrentGoal(ctx, projectID)
	if err != nil {
		return 0, err
	}
	return s.InvalidateEvidence(ctx, goal.ID, reason)
}

// VerificationRelaxation records a change that made passing easier rather than
// making the code better: a lowered threshold, a gate that stopped being
// required, deleted tests. These are legitimate sometimes, which is why they
// are recorded for review rather than blocked.
type VerificationRelaxation struct {
	ID, ProjectID, RunID, Kind, Detail string
	Before, After                      string
	CreatedAt                          time.Time
}

func (s *Store) RecordRelaxation(ctx context.Context, relaxation VerificationRelaxation) error {
	if relaxation.ProjectID == "" || relaxation.Kind == "" {
		return fmt.Errorf("project and kind are required")
	}
	if relaxation.ID == "" {
		relaxation.ID = NewID("RLX")
	}
	if relaxation.CreatedAt.IsZero() {
		relaxation.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO verification_relaxations(id,project_id,run_id,kind,detail,before_value,after_value,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		relaxation.ID, relaxation.ProjectID, relaxation.RunID, relaxation.Kind, relaxation.Detail,
		relaxation.Before, relaxation.After, relaxation.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListRelaxations(ctx context.Context, projectID string, limit int) ([]VerificationRelaxation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,COALESCE(run_id,''),kind,detail,before_value,after_value,created_at FROM verification_relaxations WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []VerificationRelaxation
	for rows.Next() {
		var relaxation VerificationRelaxation
		var created string
		if err = rows.Scan(&relaxation.ID, &relaxation.ProjectID, &relaxation.RunID, &relaxation.Kind, &relaxation.Detail,
			&relaxation.Before, &relaxation.After, &created); err != nil {
			return nil, err
		}
		relaxation.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, relaxation)
	}
	return result, rows.Err()
}

// RecordIntegrationEvidence stores verification results that belong to no
// single run: the gates executed against the merged default branch. They are
// recorded as current evidence so a goal can complete on the integrated
// result, which is the only state that actually ships.
func (s *Store) RecordIntegrationEvidence(ctx context.Context, goalID, commitSHA string, records []VerificationRecord) error {
	return s.recordExternalEvidence(ctx, goalID, "integration@"+shortSHA(commitSHA), records)
}

// RecordHumanEvidence stores gate results from a hand-edited workspace. A
// person's change is not exempt from verification; it just has no provider run
// to attach the evidence to.
func (s *Store) RecordHumanEvidence(ctx context.Context, goalID, workItemID string, records []VerificationRecord) error {
	return s.recordExternalEvidence(ctx, goalID, "human@"+workItemID, records)
}

func (s *Store) recordExternalEvidence(ctx context.Context, goalID, source string, records []VerificationRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, record := range records {
		required := 0
		if record.Required {
			required = 1
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO verification_results(goal_id,run_id,check_type,status,actual_value,command,exit_code,duration_ms,required,output,failure_kind,repair_mode,stale,stale_reason,tree_id,evaluator_id,created_at) VALUES(?,NULL,?,?,?,?,?,?,?,?,?,?,0,'',?,?,?)`,
			goalID, record.CheckType, record.Status, record.ActualValue, source,
			record.ExitCode, record.Duration.Milliseconds(), required, record.Output, record.FailureKind, record.RepairMode,
			record.TreeID, record.EvaluatorID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// StaleTreeChanged is recorded when evidence was measured against a different
// working tree than the one now in place.
const StaleTreeChanged = "measured against a different tree"

// WorkspaceTreeID pairs the workspace a measurement was taken in with the tree
// it saw. Evidence from a work item's isolated worktree is not evidence about
// the default branch, so the two are only ever compared like with like.
func WorkspaceTreeID(workspace, tree string) string {
	return workspace + "\x00" + tree
}

func splitWorkspaceTree(value string) (workspace, tree string) {
	parts := strings.SplitN(value, "\x00", 2)
	if len(parts) != 2 {
		return "", value
	}
	return parts[0], parts[1]
}

// EvaluatorID fingerprints a gate definition. Evidence carries the fingerprint
// of the gate that produced it, so a gate edited through any path — not only
// the one that remembers to invalidate — stops satisfying the criterion.
func EvaluatorID(gate GateConfig) string {
	payload := strings.Join(gate.Command, "\x00") + "\x00" + gate.SuccessValue + "\x00" + gate.ValuePattern + "\x00" + gate.Kind
	if gate.Required {
		payload += "\x00required"
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))[:16]
}

// RefreshEvidence marks evidence stale when the tree it was measured against
// or the gate that measured it no longer matches what is in place now. It is
// called wherever current state is read, so staleness is detected rather than
// depending on every mutation site remembering to declare it.
func (s *Store) RefreshEvidence(ctx context.Context, projectID, goalID, treeID string, gates []GateConfig) (int64, error) {
	evaluators := make(map[string]string, len(gates))
	for _, gate := range gates {
		evaluators[gate.Type] = EvaluatorID(gate)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,check_type,COALESCE(tree_id,''),COALESCE(evaluator_id,'') FROM verification_results WHERE goal_id=? AND stale=0 AND status='PASSED'`, goalID)
	if err != nil {
		return 0, err
	}
	type staleRow struct {
		id     int64
		reason string
	}
	var expired []staleRow
	for rows.Next() {
		var id int64
		var checkType, recordedTree, recordedEvaluator string
		if err = rows.Scan(&id, &checkType, &recordedTree, &recordedEvaluator); err != nil {
			rows.Close()
			return 0, err
		}
		switch {
		// Evidence recorded before identities were tracked cannot be judged,
		// so it is left alone rather than invalidated on a missing field.
		case recordedEvaluator != "" && evaluators[checkType] != "" && recordedEvaluator != evaluators[checkType]:
			expired = append(expired, staleRow{id, StaleGateChanged})
		default:
			// Only a measurement taken in this workspace can be invalidated by
			// this workspace moving.
			recordedWorkspace, recordedTreeOnly := splitWorkspaceTree(recordedTree)
			currentWorkspace, currentTreeOnly := splitWorkspaceTree(treeID)
			if recordedWorkspace == "" || currentWorkspace == "" || recordedWorkspace != currentWorkspace {
				continue
			}
			if recordedTreeOnly != "" && currentTreeOnly != "" && recordedTreeOnly != currentTreeOnly {
				expired = append(expired, staleRow{id, StaleTreeChanged})
			}
		}
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	var marked int64
	for _, row := range expired {
		result, execErr := s.db.ExecContext(ctx, `UPDATE verification_results SET stale=1,stale_reason=? WHERE id=? AND stale=0`, row.reason, row.id)
		if execErr != nil {
			return marked, execErr
		}
		affected, _ := result.RowsAffected()
		marked += affected
	}
	return marked, nil
}
