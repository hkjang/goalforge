package sqlite

import (
	"context"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
)

func (s *Store) RecordRunFileChanges(ctx context.Context, runID string, changes []gitops.FileChange) error {
	if runID == "" {
		return errors.New("run ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, change := range changes {
		if change.Path == "" || change.ChangeType == "" {
			return errors.New("file change path and type are required")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO run_file_changes(run_id,path,change_type,before_hash,after_hash,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(run_id,path) DO UPDATE SET change_type=excluded.change_type,before_hash=excluded.before_hash,after_hash=excluded.after_hash,created_at=excluded.created_at`, runID, change.Path, change.ChangeType, change.BeforeHash, change.AfterHash, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListRunFileChanges(ctx context.Context, runID string) ([]gitops.FileChange, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,change_type,before_hash,after_hash FROM run_file_changes WHERE run_id=? ORDER BY path`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []gitops.FileChange
	for rows.Next() {
		var change gitops.FileChange
		if err = rows.Scan(&change.Path, &change.ChangeType, &change.BeforeHash, &change.AfterHash); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}
