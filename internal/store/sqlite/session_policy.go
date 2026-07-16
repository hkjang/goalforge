package sqlite

import (
	"context"
	"errors"
	"time"
)

func (s *Store) SetSessionExpiry(ctx context.Context, providerName, sessionID string, expiresAt time.Time) error {
	if providerName == "" || sessionID == "" || expiresAt.IsZero() {
		return errors.New("provider, session ID, and expiry are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE provider_session_history SET expires_at=?,updated_at=? WHERE provider=? AND session_id=? AND status='ACTIVE'`, expiresAt.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), providerName, sessionID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) InvalidateSession(ctx context.Context, projectID, providerName, sessionID, reason string, retention time.Duration) error {
	if projectID == "" || providerName == "" || sessionID == "" || reason == "" || retention < 0 {
		return errors.New("project, provider, session, reason, and non-negative retention are required")
	}
	now := time.Now().UTC()
	until := now.Add(retention).Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE provider_session_history SET status='INVALID',updated_at=?,retention_until=?,replacement_reason=? WHERE project_id=? AND provider=? AND session_id=? AND status='ACTIVE'`, now.Format(time.RFC3339Nano), until, reason, projectID, providerName, sessionID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `UPDATE provider_sessions SET status='INVALID',updated_at=? WHERE project_id=? AND provider=? AND session_id=?`, now.Format(time.RFC3339Nano), projectID, providerName, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PruneSessions(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM provider_session_history WHERE status<>'ACTIVE' AND retention_until IS NOT NULL AND retention_until<=?`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
