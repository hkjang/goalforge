package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
)

type WorktreeRecord struct {
	ProjectID, WorkItemID, Path, Branch, BaseCommit, Status string
}

func (s *Store) RecordWorktree(ctx context.Context, projectID, workItemID string, worktree gitops.Worktree) error {
	if projectID == "" || workItemID == "" || worktree.Path == "" || worktree.Branch == "" {
		return errors.New("project, work item, path, and branch are required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO worktrees(project_id,work_item_id,path,branch,base_commit,status,created_at,updated_at) VALUES(?,?,?,?,?,'ACTIVE',?,?) ON CONFLICT(project_id,work_item_id) DO UPDATE SET path=excluded.path,branch=excluded.branch,base_commit=excluded.base_commit,status='ACTIVE',updated_at=excluded.updated_at`, projectID, workItemID, worktree.Path, worktree.Branch, worktree.BaseCommit, now, now)
	return err
}

func (s *Store) WorktreeForWorkItem(ctx context.Context, projectID, workItemID string) (WorktreeRecord, error) {
	var record WorktreeRecord
	err := s.db.QueryRowContext(ctx, `SELECT project_id,work_item_id,path,branch,base_commit,status FROM worktrees WHERE project_id=? AND work_item_id=? AND status='ACTIVE'`, projectID, workItemID).Scan(&record.ProjectID, &record.WorkItemID, &record.Path, &record.Branch, &record.BaseCommit, &record.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	return record, err
}

// WorktreesForCleanup returns ACTIVE worktrees whose work item reached a
// terminal status, i.e. safe garbage-collection candidates.
func (s *Store) WorktreesForCleanup(ctx context.Context, projectID string) ([]WorktreeRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.project_id,w.work_item_id,w.path,w.branch,w.base_commit,w.status FROM worktrees w JOIN work_items i ON i.id=w.work_item_id WHERE w.project_id=? AND w.status='ACTIVE' AND i.status IN ('DONE','DISCARDED') ORDER BY w.work_item_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []WorktreeRecord
	for rows.Next() {
		var record WorktreeRecord
		if err = rows.Scan(&record.ProjectID, &record.WorkItemID, &record.Path, &record.Branch, &record.BaseCommit, &record.Status); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) MarkWorktreeRemoved(ctx context.Context, projectID, workItemID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE worktrees SET status='REMOVED',updated_at=? WHERE project_id=? AND work_item_id=? AND status='ACTIVE'`, time.Now().UTC().Format(time.RFC3339Nano), projectID, workItemID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) LatestRunFileChangesForWork(ctx context.Context, projectID, workItemID string) ([]gitops.FileChange, error) {
	var runID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM runs WHERE project_id=? AND work_item_id=? ORDER BY started_at DESC,id DESC LIMIT 1`, projectID, workItemID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.ListRunFileChanges(ctx, runID)
}

func (s *Store) RecordRollback(ctx context.Context, projectID, workItemID string, worktree WorktreeRecord, reason string) error {
	if reason == "" {
		return errors.New("rollback reason is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO rollback_records(id,project_id,work_item_id,target_commit,path,branch,reason,created_at) VALUES(?,?,?,?,?,?,?,?)`, NewID("ROLLBACK"), projectID, workItemID, worktree.BaseCommit, worktree.Path, worktree.Branch, reason, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE id=? AND status NOT IN ('DONE','DISCARDED')`, workItemID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET state='READY' WHERE id=? AND state IN ('BLOCKED','FAILED','REPAIR_REQUIRED')`, projectID); err != nil {
		return err
	}
	return tx.Commit()
}
