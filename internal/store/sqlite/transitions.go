package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/policy"
)

// Who may move a work item into a state.
//
// The engine owns the states that assert something actually happened:
// IN_PROGRESS means a run is executing, VERIFYING means gates are running, and
// DONE means they passed. A person moving a card into DONE would be asserting
// a verification that never ran — which is precisely what a board with
// drag-and-drop makes easy, and precisely what must not be possible.
//
// Everything else is a judgement a person is entitled to make: what to work on
// next, what to set aside, what to drop.
var manualStatuses = map[string]bool{
	"BACKLOG": true, "APPROVED": true, "BLOCKED": true, "DISCARDED": true,
}

// enginePhases are the states a run is passing through. A person cannot move
// an item out of one: the run is still going, and the way to stop it is to
// stop it.
var enginePhases = map[string]bool{"IN_PROGRESS": true, "VERIFYING": true}

// ErrStaleWorkItem means the item changed since it was read. It is a distinct
// error because the remedy is to look again, not to retry.
var ErrStaleWorkItem = errors.New("work item changed since it was loaded")

// TransitionRefusal explains why a move is not allowed, in the terms the
// person made it. A board that greys out a column without saying why leaves
// the user guessing at a rule they cannot see.
type TransitionRefusal struct{ From, To, Reason string }

func (e *TransitionRefusal) Error() string { return e.Reason }

// CheckManualTransition reports whether a person may move an item from one
// status to another, and why not when they may not.
//
// It is shared by the CLI, the API, and MCP so a rule enforced on one surface
// is enforced on all of them. It was not: the API refused anything outside
// triage while the CLI would set any status, so `work status ID --set DONE`
// marked an item verified without a gate ever running.
func CheckManualTransition(from, to string) error {
	if from == to {
		return &TransitionRefusal{From: from, To: to, Reason: "이미 " + statusLabelKo(to) + " 상태입니다"}
	}
	if !manualStatuses[to] {
		return &TransitionRefusal{From: from, To: to,
			Reason: policy.Topic(statusLabelKo(to)) + " 실행·검증 엔진이 정합니다. 사람이 옮기면 하지 않은 검증을 했다고 주장하는 것이 됩니다"}
	}
	if enginePhases[from] {
		return &TransitionRefusal{From: from, To: to,
			Reason: statusLabelKo(from) + " 인 작업은 실행이 끝나야 옮길 수 있습니다 (goalforge cancel 로 중단하세요)"}
	}
	return nil
}

// AllowedManualTargets lists the columns a person may drag this item into, so
// the board can show the same answer the server would give.
func AllowedManualTargets(from string) []string {
	var allowed []string
	for status := range manualStatuses {
		if CheckManualTransition(from, status) == nil {
			allowed = append(allowed, status)
		}
	}
	sort.Strings(allowed)
	return allowed
}

// RefusedManualTargets pairs each disallowed column with its reason, which is
// what turns a greyed-out column into something the user can act on.
//
// It covers every column on the board, not only the ones a person may use: a
// drag can be attempted anywhere, and the engine-owned columns are exactly the
// ones whose refusal a user most needs explained.
func RefusedManualTargets(from string) map[string]string {
	refusals := map[string]string{}
	for _, status := range BoardStatuses {
		if err := CheckManualTransition(from, status); err != nil {
			refusals[status] = err.Error()
		}
	}
	return refusals
}

// ApplyManualTransition moves a work item as a person, refusing what a person
// may not do and refusing a change based on a stale read.
//
// expectedVersion of zero means "whatever it is now", for callers that have no
// version to offer. A caller that does have one gets the conflict rather than
// overwriting somebody else's edit or the engine's own state change.
func (s *Store) ApplyManualTransition(ctx context.Context, goalID, workID, to string, expectedVersion int64) (model.WorkItem, error) {
	var item model.WorkItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	var from string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT status,COALESCE(version,1) FROM work_items WHERE id=? AND goal_id=?`, workID, goalID).
		Scan(&from, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	if expectedVersion > 0 && expectedVersion != version {
		return item, fmt.Errorf("%w: 읽은 버전 %d, 현재 버전 %d", ErrStaleWorkItem, expectedVersion, version)
	}
	if err = CheckManualTransition(from, to); err != nil {
		return item, err
	}
	if err = s.setWorkItemStatusTx(ctx, tx, goalID, workID, to); err != nil {
		return item, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_items SET version=COALESCE(version,1)+1 WHERE id=? AND goal_id=?`, workID, goalID); err != nil {
		return item, err
	}
	if err = tx.Commit(); err != nil {
		return item, err
	}
	return s.WorkItemByID(ctx, goalID, workID)
}

func statusLabelKo(status string) string {
	labels := map[string]string{"BACKLOG": "백로그", "APPROVED": "실행 대기", "IN_PROGRESS": "진행 중",
		"VERIFYING": "검증 중", "DONE": "작업 완료", "BLOCKED": "보류", "DISCARDED": "폐기"}
	if label, ok := labels[status]; ok {
		return label
	}
	return status
}

// BoardStatuses are the board's columns in the order work moves through them.
var BoardStatuses = []string{"BACKLOG", "APPROVED", "IN_PROGRESS", "VERIFYING", "DONE", "BLOCKED", "DISCARDED"}

// StatusLabel is the column heading for a status.
func StatusLabel(status string) string { return statusLabelKo(status) }

// ManualStatus reports whether a person may move an item into this status.
func ManualStatus(status string) bool { return manualStatuses[strings.ToUpper(status)] }

// ApplyAutomaticTransition is ApplyManualTransition for the autonomy policy.
//
// It goes through the same rule rather than writing the status directly: the
// engine's own states stay the engine's, and automation acting on a person's
// behalf is held to what a person could have done. The separate entry point
// exists so the two are distinguishable in a stack trace and so this one can
// never be reached from a request handler by accident.
func (s *Store) ApplyAutomaticTransition(ctx context.Context, goalID, workID, to string, expectedVersion int64) (model.WorkItem, error) {
	return s.ApplyManualTransition(ctx, goalID, workID, to, expectedVersion)
}
