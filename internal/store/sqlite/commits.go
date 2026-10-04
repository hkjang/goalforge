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

// LatestRunCommitForWork returns the newest verified commit recorded for a
// work item; only such commits are eligible for publishing.
func (s *Store) LatestRunCommitForWork(ctx context.Context, projectID, workItemID string) (RunCommit, error) {
	var commit RunCommit
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,project_id,goal_id,work_item_id,commit_sha,branch,files_committed,created_at FROM run_commits WHERE project_id=? AND work_item_id=? ORDER BY created_at DESC,run_id DESC LIMIT 1`, projectID, workItemID).
		Scan(&commit.RunID, &commit.ProjectID, &commit.GoalID, &commit.WorkItemID, &commit.CommitSHA, &commit.Branch, &commit.FilesCommitted, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return commit, ErrNotFound
	}
	if err == nil {
		commit.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	}
	return commit, err
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

// LatestGoalCommit is the newest verified commit produced for a goal.
//
// It is the base for the next work item's worktree. Every worktree used to be
// cut from the default branch, so an item could not see what earlier items had
// built: item 1 wrote store/, verified, and was marked done, and item 3's
// worktree — cut from a main nothing had been merged into — failed with
// "stat …/store: directory not found". Every item that depended on earlier work
// failed, and the chain could not build a program made of more than one piece.
//
// The approval boundary does not move. That boundary is about the default
// branch, which still nothing reaches without an approval; this is about where
// unmerged work continues from.
//
// Discarded work is skipped: somebody decided not to do it, and building on top
// would carry it in regardless.
func (s *Store) LatestGoalCommit(ctx context.Context, projectID, goalID string) (RunCommit, error) {
	var commit RunCommit
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT c.run_id,c.project_id,c.goal_id,c.work_item_id,c.commit_sha,c.branch,c.files_committed,c.created_at
FROM run_commits c JOIN work_items w ON w.id=c.work_item_id
WHERE c.project_id=? AND c.goal_id=? AND w.status<>'DISCARDED'
ORDER BY c.created_at DESC,c.run_id DESC LIMIT 1`, projectID, goalID).
		Scan(&commit.RunID, &commit.ProjectID, &commit.GoalID, &commit.WorkItemID, &commit.CommitSHA,
			&commit.Branch, &commit.FilesCommitted, &created)
	if errors.Is(err, sql.ErrNoRows) {
		// Reported as not found rather than as an empty commit: an empty SHA
		// handed to `git worktree add` is a different failure with a worse
		// message, and the caller's fallback is the default branch.
		return commit, ErrNotFound
	}
	if err == nil {
		commit.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	}
	return commit, err
}
