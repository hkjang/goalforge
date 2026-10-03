package sqlite

import (
	"context"
	"time"
)

// MergeWaiting is one work item whose verified commit has not reached the
// default branch.
//
// It exists because nothing reported this. Each work item is implemented and
// verified in its own worktree and nothing merges them, so an operator who did
// not already know to run `goalforge merge --work-item ID` per item merged the
// code by hand — and the goal could not complete, because the criteria are
// measured on the branch that ships and nothing had been put there.
type MergeWaiting struct {
	WorkItemID, Title, Status string
	Branch, CommitSHA, RunID  string
	CommittedAt               time.Time
}

// WorkWaitingToMerge lists the verified commits that have not been merged.
//
// The answer comes from the merge effect, not from the work item's status: the
// effect is keyed on the commit and records whether the merge actually
// happened. A status would say what somebody intended.
func (s *Store) WorkWaitingToMerge(ctx context.Context, projectID, goalID string) ([]MergeWaiting, error) {
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.title,w.status FROM work_items w
WHERE w.goal_id=? AND w.status<>'DISCARDED' ORDER BY w.priority DESC,w.id`, goalID)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, title, status string }
	var candidates []candidate
	for rows.Next() {
		var entry candidate
		if err = rows.Scan(&entry.id, &entry.title, &entry.status); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, entry)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	var waiting []MergeWaiting
	for _, entry := range candidates {
		commit, commitErr := s.LatestRunCommitForWork(ctx, projectID, entry.id)
		if commitErr != nil {
			// No verified commit means nothing to merge. Listing it would send
			// somebody to merge work that does not exist.
			continue
		}
		key := EffectKey(EffectMergeBranch, projectID, entry.id, project.DefaultBranch, commit.CommitSHA)
		effect, effectErr := s.EffectByKey(ctx, key)
		if effectErr == nil && effect.State == EffectSucceeded {
			continue
		}
		// An attempted merge that failed is still waiting. Treating a recorded
		// attempt as done would hide exactly the item somebody has to look at.
		waiting = append(waiting, MergeWaiting{WorkItemID: entry.id, Title: entry.title,
			Status: entry.status, Branch: commit.Branch, CommitSHA: commit.CommitSHA,
			RunID: commit.RunID, CommittedAt: commit.CreatedAt})
	}
	return waiting, nil
}
