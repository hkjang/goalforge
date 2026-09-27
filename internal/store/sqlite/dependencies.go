package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/goalforge/goalforge/internal/policy"
)

// dependencyRows loads every declared predecessor for a set of work items in
// one query, so listing a backlog does not issue a query per item.
func (s *Store) dependencyRows(ctx context.Context, goalID string) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.work_item_id,d.depends_on_id FROM work_item_dependencies d JOIN work_items w ON w.id=d.work_item_id WHERE w.goal_id=? ORDER BY d.work_item_id,d.depends_on_id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]string{}
	for rows.Next() {
		var workID, dependsOn string
		if err = rows.Scan(&workID, &dependsOn); err != nil {
			return nil, err
		}
		result[workID] = append(result[workID], dependsOn)
	}
	return result, rows.Err()
}

func (s *Store) dependenciesOf(ctx context.Context, q queryer, workID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT depends_on_id FROM work_item_dependencies WHERE work_item_id=? ORDER BY depends_on_id`, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var dependsOn string
		if err = rows.Scan(&dependsOn); err != nil {
			return nil, err
		}
		result = append(result, dependsOn)
	}
	return result, rows.Err()
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// setDependencies replaces a work item's predecessors, refusing anything that
// would create a cycle. A dependency cycle is not a slow plan, it is a plan
// that can never start.
func (s *Store) setDependencies(ctx context.Context, q queryer, goalID, workID string, dependencies []string) error {
	unique := make([]string, 0, len(dependencies))
	seen := map[string]bool{}
	for _, dependency := range dependencies {
		dependency = strings.TrimSpace(dependency)
		if dependency == "" || seen[dependency] {
			continue
		}
		if dependency == workID {
			return errors.New("a work item cannot depend on itself")
		}
		var exists string
		if err := q.QueryRowContext(ctx, `SELECT id FROM work_items WHERE id=? AND goal_id=?`, dependency, goalID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("dependency %s is not a work item of this goal", dependency)
		} else if err != nil {
			return err
		}
		seen[dependency] = true
		unique = append(unique, dependency)
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM work_item_dependencies WHERE work_item_id=?`, workID); err != nil {
		return err
	}
	for _, dependency := range unique {
		if _, err := q.ExecContext(ctx, `INSERT INTO work_item_dependencies(work_item_id,depends_on_id) VALUES(?,?)`, workID, dependency); err != nil {
			return err
		}
	}
	cycle, err := s.findCycle(ctx, q, goalID, workID)
	if err != nil {
		return err
	}
	if len(cycle) > 0 {
		return fmt.Errorf("dependency cycle: %s", strings.Join(cycle, " -> "))
	}
	return nil
}

// findCycle walks predecessors depth-first from start and returns the path of
// the first cycle it closes.
func (s *Store) findCycle(ctx context.Context, q queryer, goalID, start string) ([]string, error) {
	type frame struct {
		node string
		path []string
	}
	visited := map[string]bool{}
	stack := []frame{{node: start, path: []string{start}}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		predecessors, err := s.dependenciesOf(ctx, q, current.node)
		if err != nil {
			return nil, err
		}
		for _, predecessor := range predecessors {
			if predecessor == start {
				return append(append([]string{}, current.path...), start), nil
			}
			if visited[predecessor] {
				continue
			}
			visited[predecessor] = true
			stack = append(stack, frame{node: predecessor, path: append(append([]string{}, current.path...), predecessor)})
		}
	}
	return nil, nil
}

// unmetDependencies returns the predecessors that are not DONE, which is what
// the user needs to see rather than "blocked".
func (s *Store) unmetDependencies(ctx context.Context, q queryer, workID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT d.depends_on_id FROM work_item_dependencies d LEFT JOIN work_items w ON w.id=d.depends_on_id WHERE d.work_item_id=? AND COALESCE(w.status,'')<>'DONE' ORDER BY d.depends_on_id`, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var dependency string
		if err = rows.Scan(&dependency); err != nil {
			return nil, err
		}
		result = append(result, dependency)
	}
	return result, rows.Err()
}

// activeWork is one work item currently being implemented, with who holds it.
type activeWork struct {
	ID, Scope, Owner string
}

// activeWorkItems lists what is being implemented right now. Human-held items
// are included: a person editing a workspace conflicts with an agent editing
// the same files just as much as two agents would.
func (s *Store) activeWorkItems(ctx context.Context, q queryer, goalID, excludeID string) ([]activeWork, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,change_scope,COALESCE(owner,'AI') FROM work_items WHERE goal_id=? AND status='IN_PROGRESS' AND id<>?`, goalID, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []activeWork
	for rows.Next() {
		var item activeWork
		if err = rows.Scan(&item.ID, &item.Scope, &item.Owner); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// wipLimit reads the project's implementation concurrency ceiling.
func (s *Store) wipLimit(ctx context.Context, q queryer, goalID string) (int, error) {
	var limit int
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(p.wip_limit,1) FROM goals g JOIN projects p ON p.id=g.project_id WHERE g.id=?`, goalID).Scan(&limit); err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 1
	}
	return limit, nil
}

// scopeConflicts returns the active items a scope could collide with.
func scopeConflicts(scope string, active []activeWork) []string {
	var conflicts []string
	for _, item := range active {
		if policy.ScopesOverlap(scope, item.Scope) {
			label := fmt.Sprintf("%s (%s)", item.ID, scopeLabel(item.Scope))
			if item.Owner == OwnerHuman {
				label += " — 사람이 수정 중"
			}
			conflicts = append(conflicts, label)
		}
	}
	return conflicts
}

// checkConcurrency enforces the project's implementation WIP limit and refuses
// to start work whose declared scope overlaps something already in progress.
// Two sessions editing the same files in separate worktrees produce a conflict
// that neither of them verified.
//
// Only agent-held items count toward the limit: handing one item to a person
// is not a reason for automation to stop working on everything else, and the
// scope check still keeps it out of that person's files.
func (s *Store) checkConcurrency(ctx context.Context, q queryer, goalID, workID string) error {
	limit, err := s.wipLimit(ctx, q, goalID)
	if err != nil {
		return err
	}
	var scope string
	if err = q.QueryRowContext(ctx, `SELECT change_scope FROM work_items WHERE id=? AND goal_id=?`, workID, goalID).Scan(&scope); err != nil {
		return err
	}
	active, err := s.activeWorkItems(ctx, q, goalID, workID)
	if err != nil {
		return err
	}
	agentHeld := 0
	for _, item := range active {
		if item.Owner != OwnerHuman {
			agentHeld++
		}
	}
	if agentHeld >= limit {
		return fmt.Errorf("implementation WIP limit reached: %d of %d items in progress", agentHeld, limit)
	}
	if conflicts := scopeConflicts(scope, active); len(conflicts) > 0 {
		return fmt.Errorf("change scope %s overlaps work already in progress: %s", scopeLabel(scope), strings.Join(conflicts, ", "))
	}
	return nil
}

func scopeLabel(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return "(범위 미지정)"
	}
	return scope
}

// SetWIPLimit changes how many work items a project may implement at once.
// Above one, only items with disjoint declared scopes can actually run
// together; the limit is a ceiling, not a promise.
func (s *Store) SetWIPLimit(ctx context.Context, projectID string, limit int) error {
	if limit < 1 || limit > 8 {
		return errors.New("WIP limit must be between 1 and 8")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET wip_limit=? WHERE id=?`, limit, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}
