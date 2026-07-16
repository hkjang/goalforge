package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type GateConfig struct {
	Type         string
	Command      []string
	Timeout      time.Duration
	Required     bool
	SuccessValue string
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO verification_gates(project_id,check_type,command_json,timeout_seconds,required,success_value,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id,check_type) DO UPDATE SET command_json=excluded.command_json,timeout_seconds=excluded.timeout_seconds,required=excluded.required,success_value=excluded.success_value`, projectID, g.Type, string(raw), int64(g.Timeout/time.Second), required, g.SuccessValue, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
func (s *Store) ListGates(ctx context.Context, projectID string) ([]GateConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT check_type,command_json,timeout_seconds,required,success_value FROM verification_gates WHERE project_id=? ORDER BY check_type`, projectID)
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
		if err = rows.Scan(&g.Type, &raw, &seconds, &required, &g.SuccessValue); err != nil {
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
