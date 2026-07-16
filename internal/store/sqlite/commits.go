package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RunCommit records the commit created for a verified run (GIT-009).
type RunCommit struct {
	RunID, ProjectID, GoalID, WorkItemID string
	CommitSHA, Branch                    string
	FilesCommitted                       int
	CreatedAt                            time.Time
}

func (s *Store) RecordRunCommit(ctx context.Context, commit RunCommit) error {
	if commit.RunID == "" || commit.ProjectID == "" || commit.GoalID == "" || commit.WorkItemID == "" || commit.CommitSHA == "" {
		return errors.New("run, project, goal, work item, and commit SHA are required")
	}
	if commit.CreatedAt.IsZero() {
		commit.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_commits(run_id,project_id,goal_id,work_item_id,commit_sha,branch,files_committed,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		commit.RunID, commit.ProjectID, commit.GoalID, commit.WorkItemID, commit.CommitSHA, commit.Branch, commit.FilesCommitted, commit.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) RunCommitByRun(ctx context.Context, runID string) (RunCommit, error) {
	var commit RunCommit
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,project_id,goal_id,work_item_id,commit_sha,branch,files_committed,created_at FROM run_commits WHERE run_id=?`, runID).
		Scan(&commit.RunID, &commit.ProjectID, &commit.GoalID, &commit.WorkItemID, &commit.CommitSHA, &commit.Branch, &commit.FilesCommitted, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return commit, ErrNotFound
	}
	if err == nil {
		commit.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	}
	return commit, err
}
