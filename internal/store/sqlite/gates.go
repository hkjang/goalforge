package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

type GateConfig struct {
	Type         string
	Command      []string
	Timeout      time.Duration
	Required     bool
	SuccessValue string
	// ValuePattern extracts the measured value from the gate output with a
	// single capture group, so numeric criteria are proven by measurement
	// rather than by the configured SuccessValue being echoed back.
	ValuePattern string
}

func (s *Store) UpsertGate(ctx context.Context, projectID string, g GateConfig) error {
	if projectID == "" || g.Type == "" || len(g.Command) == 0 || g.Timeout <= 0 {
		return errors.New("project, gate type, command, and positive timeout are required")
	}
	raw, err := json.Marshal(g.Command)
	if err != nil {
		return err
	}
	required := 0
	if g.Required {
		required = 1
	}
	if g.SuccessValue == "" {
		g.SuccessValue = "true"
	}
	if g.ValuePattern != "" {
		pattern, compileErr := regexp.Compile(g.ValuePattern)
		if compileErr != nil {
			return fmt.Errorf("gate value pattern is not a valid regular expression: %w", compileErr)
		}
		if pattern.NumSubexp() < 1 {
			return errors.New("gate value pattern needs one capture group around the measured value")
		}
	}
	previous, previousErr := s.gateByType(ctx, projectID, g.Type)
	if previousErr != nil && !errors.Is(previousErr, ErrNotFound) {
		return previousErr
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO verification_gates(project_id,check_type,command_json,timeout_seconds,required,success_value,value_pattern,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(project_id,check_type) DO UPDATE SET command_json=excluded.command_json,timeout_seconds=excluded.timeout_seconds,required=excluded.required,success_value=excluded.success_value,value_pattern=excluded.value_pattern`, projectID, g.Type, string(raw), int64(g.Timeout/time.Second), required, g.SuccessValue, g.ValuePattern, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if errors.Is(previousErr, ErrNotFound) {
		return nil
	}
	// Changing the check invalidates what the old check proved, and a change
	// that only makes passing easier is recorded for review: relaxing the
	// standard is a legitimate decision, but it must not look like progress.
	if previous.SuccessValue != g.SuccessValue || !equalCommands(previous.Command, g.Command) || previous.Required != g.Required || previous.ValuePattern != g.ValuePattern {
		if _, staleErr := s.invalidateGateEvidence(ctx, projectID, g.Type); staleErr != nil {
			return staleErr
		}
	}
	return s.recordGateRelaxation(ctx, projectID, previous, g)
}

func (s *Store) gateByType(ctx context.Context, projectID, checkType string) (GateConfig, error) {
	var g GateConfig
	var raw string
	var seconds int64
	var required int
	err := s.db.QueryRowContext(ctx, `SELECT check_type,command_json,timeout_seconds,required,success_value,value_pattern FROM verification_gates WHERE project_id=? AND check_type=?`, projectID, checkType).
		Scan(&g.Type, &raw, &seconds, &required, &g.SuccessValue, &g.ValuePattern)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, err
	}
	if err = json.Unmarshal([]byte(raw), &g.Command); err != nil {
		return g, err
	}
	g.Timeout = time.Duration(seconds) * time.Second
	g.Required = required == 1
	return g, nil
}

func (s *Store) invalidateGateEvidence(ctx context.Context, projectID, checkType string) (int64, error) {
	goal, err := s.CurrentGoal(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return s.InvalidateEvidence(ctx, goal.ID, StaleGateChanged, checkType)
}

// recordGateRelaxation notes the specific ways a gate got easier to pass.
func (s *Store) recordGateRelaxation(ctx context.Context, projectID string, previous, current GateConfig) error {
	if previous.Required && !current.Required {
		if err := s.RecordRelaxation(ctx, VerificationRelaxation{ProjectID: projectID, Kind: "gate_optional",
			Detail: current.Type + " 게이트가 필수에서 선택으로 바뀌었습니다", Before: "required", After: "optional"}); err != nil {
			return err
		}
	}
	before, beforeErr := strconv.ParseFloat(previous.SuccessValue, 64)
	after, afterErr := strconv.ParseFloat(current.SuccessValue, 64)
	if beforeErr == nil && afterErr == nil && after < before {
		return s.RecordRelaxation(ctx, VerificationRelaxation{ProjectID: projectID, Kind: "threshold_lowered",
			Detail: current.Type + " 기준값이 낮아졌습니다", Before: previous.SuccessValue, After: current.SuccessValue})
	}
	if beforeErr != nil && afterErr != nil && previous.SuccessValue != current.SuccessValue {
		return s.RecordRelaxation(ctx, VerificationRelaxation{ProjectID: projectID, Kind: "criterion_changed",
			Detail: current.Type + " 성공 기준이 바뀌었습니다", Before: previous.SuccessValue, After: current.SuccessValue})
	}
	return nil
}

func equalCommands(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func (s *Store) ListGates(ctx context.Context, projectID string) ([]GateConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT check_type,command_json,timeout_seconds,required,success_value,value_pattern FROM verification_gates WHERE project_id=? ORDER BY check_type`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []GateConfig
	for rows.Next() {
		var g GateConfig
		var raw string
		var seconds int64
		var required int
		if err = rows.Scan(&g.Type, &raw, &seconds, &required, &g.SuccessValue, &g.ValuePattern); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &g.Command); err != nil {
			return nil, err
		}
		g.Timeout = time.Duration(seconds) * time.Second
		g.Required = required == 1
		result = append(result, g)
	}
	return result, rows.Err()
}
