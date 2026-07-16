package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type RuntimePolicy struct {
	TurnTimeout, RunTimeout time.Duration
}

func DefaultRuntimePolicy() RuntimePolicy {
	return RuntimePolicy{TurnTimeout: 30 * time.Minute, RunTimeout: 2 * time.Hour}
}

func (s *Store) SetRuntimePolicy(ctx context.Context, projectID string, policy RuntimePolicy) error {
	if projectID == "" || policy.TurnTimeout <= 0 || policy.RunTimeout <= 0 || policy.TurnTimeout > policy.RunTimeout {
		return errors.New("project and positive timeouts satisfying turn <= run are required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runtime_policies(project_id,turn_timeout_seconds,run_timeout_seconds,updated_at) VALUES(?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET turn_timeout_seconds=excluded.turn_timeout_seconds,run_timeout_seconds=excluded.run_timeout_seconds,updated_at=excluded.updated_at`, projectID, int64(policy.TurnTimeout/time.Second), int64(policy.RunTimeout/time.Second), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) RuntimePolicy(ctx context.Context, projectID string) (RuntimePolicy, error) {
	var turn, run int64
	err := s.db.QueryRowContext(ctx, `SELECT turn_timeout_seconds,run_timeout_seconds FROM runtime_policies WHERE project_id=?`, projectID).Scan(&turn, &run)
	if errors.Is(err, sql.ErrNoRows) {
		return RuntimePolicy{}, ErrNotFound
	}
	if err != nil {
		return RuntimePolicy{}, err
	}
	return RuntimePolicy{TurnTimeout: time.Duration(turn) * time.Second, RunTimeout: time.Duration(run) * time.Second}, nil
}
