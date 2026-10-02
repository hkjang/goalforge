package sqlite

import (
	"context"
	"fmt"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/policy"
)

// SetWorkItemScope corrects the change scope of one work item.
//
// It exists because a work item whose scope is not a list of path patterns can
// never run — every file the session writes is reported as out of scope — and
// there was no way to fix one. The only remedy was to discard the item and
// write it again, which loses everything else recorded against it.
//
// The new scope is checked here. Replacing an unusable scope with another
// unusable one leaves the item exactly as stuck, having told the operator it
// was fixed.
func (s *Store) SetWorkItemScope(ctx context.Context, goalID, workID, scope string) (model.WorkItem, error) {
	var item model.WorkItem
	if !policy.UsableScope(scope) {
		return item, fmt.Errorf("%q 는 경로 목록이 아닙니다 — 바꿀 파일 경로나 glob 을 쉼표로 구분해 적어야 합니다 (예: internal/server/handler.go 또는 internal/store/**,cmd/app/main.go)", scope)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE work_items SET change_scope=? WHERE id=? AND goal_id=?`,
		scope, workID, goalID)
	if err != nil {
		return item, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return item, ErrNotFound
	}
	return s.WorkItemByID(ctx, goalID, workID)
}

// UnusableScopeItems lists the work items that can never run because their
// scope is not a path list.
//
// Reported as a group because they arrive as a group: a generator that was not
// told the format produced a whole batch of them, and fixing them one at a
// time starts with finding them.
func (s *Store) UnusableScopeItems(ctx context.Context, goalID string) ([]model.WorkItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,change_scope,status FROM work_items
WHERE goal_id=? AND status NOT IN ('DONE','DISCARDED') ORDER BY priority DESC,id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stuck []model.WorkItem
	for rows.Next() {
		var item model.WorkItem
		if err = rows.Scan(&item.ID, &item.Title, &item.ChangeScope, &item.Status); err != nil {
			return nil, err
		}
		// An empty scope is a separate decision: OutOfScopeChanges reads it as
		// "may change nothing", which is restrictive but deliberate, and it is
		// not the malformed case this is for.
		if item.ChangeScope == "" || policy.UsableScope(item.ChangeScope) {
			continue
		}
		item.GoalID = goalID
		stuck = append(stuck, item)
	}
	return stuck, rows.Err()
}
