package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func boardFixture(t *testing.T) (context.Context, *Store, model.Project, model.Goal) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo",
		DefaultBranch: "main", Provider: "codex", WIPLimit: 1}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, project.ID, "G", "o", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, s, project, goal
}

func seedItem(t *testing.T, ctx context.Context, s *Store, goal model.Goal, id, title, status string, priority float64) {
	t.Helper()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO work_items(id,goal_id,type,title,status,weight,priority,version) VALUES(?,?,'IMPLEMENT',?,?,1,?,1)`,
		id, goal.ID, title, status, priority); err != nil {
		t.Fatal(err)
	}
}

// The one thing a drag must never do. DONE means gates ran and passed; a
// person moving a card there asserts a verification that never happened.
func TestAPersonCannotMarkWorkDone(t *testing.T) {
	for _, from := range []string{"BACKLOG", "APPROVED", "BLOCKED", "VERIFYING"} {
		err := CheckManualTransition(from, "DONE")
		if err == nil {
			t.Fatalf("%s -> DONE must be refused", from)
		}
		var refusal *TransitionRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("the refusal must be typed so the board can show it: %T", err)
		}
		if refusal.Reason == "" {
			t.Fatal("a greyed-out column without a reason leaves the user guessing")
		}
	}
	// Nor the other states a run passes through.
	for _, to := range []string{"IN_PROGRESS", "VERIFYING"} {
		if CheckManualTransition("BACKLOG", to) == nil {
			t.Fatalf("BACKLOG -> %s must be refused", to)
		}
	}
}

// The judgements a person is entitled to make stay theirs.
func TestAPersonMayTriage(t *testing.T) {
	for _, to := range []string{"APPROVED", "BLOCKED", "DISCARDED"} {
		if err := CheckManualTransition("BACKLOG", to); err != nil {
			t.Errorf("BACKLOG -> %s must be allowed: %v", to, err)
		}
	}
	if err := CheckManualTransition("DONE", "BACKLOG"); err != nil {
		t.Errorf("reopening finished work is a human decision: %v", err)
	}
}

// A run in flight is stopped by stopping it, not by moving its card.
func TestWorkInFlightCannotBeDragged(t *testing.T) {
	err := CheckManualTransition("IN_PROGRESS", "BLOCKED")
	if err == nil {
		t.Fatal("an executing item must not be moved out from under the run")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("the refusal must say what to do instead: %v", err)
	}
}

// The board must offer exactly what the server will accept, or a drag lands
// and then bounces.
func TestOfferedColumnsMatchWhatTheServerAccepts(t *testing.T) {
	for _, from := range BoardStatuses {
		allowed := AllowedManualTargets(from)
		refused := RefusedManualTargets(from)
		for _, to := range allowed {
			if err := CheckManualTransition(from, to); err != nil {
				t.Errorf("%s -> %s was offered but is refused: %v", from, to, err)
			}
			if _, both := refused[to]; both {
				t.Errorf("%s -> %s is both offered and refused", from, to)
			}
		}
		for to, reason := range refused {
			if CheckManualTransition(from, to) == nil {
				t.Errorf("%s -> %s is refused on the board but allowed by the server", from, to)
			}
			if reason == "" {
				t.Errorf("%s -> %s refused without a reason", from, to)
			}
		}
	}
}

// The rule is enforced where the change happens, not only where the board
// draws it: a caller reaching the store directly must be refused too.
func TestManualTransitionIsEnforcedAtTheStore(t *testing.T) {
	ctx, s, _, goal := boardFixture(t)
	seedItem(t, ctx, s, goal, "W1", "작업", "BACKLOG", 50)
	if _, err := s.ApplyManualTransition(ctx, goal.ID, "W1", "DONE", 0); err == nil {
		t.Fatal("the store must refuse what the board refuses")
	}
	item, err := s.ApplyManualTransition(ctx, goal.ID, "W1", "APPROVED", 0)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "APPROVED" {
		t.Fatalf("status=%s", item.Status)
	}
}

// An edit made against a stale view must be refused rather than overwriting
// whatever happened in between — another person's change, or the engine's.
func TestStaleEditIsRefused(t *testing.T) {
	ctx, s, _, goal := boardFixture(t)
	seedItem(t, ctx, s, goal, "W1", "작업", "BACKLOG", 50)
	if _, err := s.ApplyManualTransition(ctx, goal.ID, "W1", "APPROVED", 1); err != nil {
		t.Fatal(err)
	}
	// A second board still holding version 1 tries to move it elsewhere.
	_, err := s.ApplyManualTransition(ctx, goal.ID, "W1", "BLOCKED", 1)
	if !errors.Is(err, ErrStaleWorkItem) {
		t.Fatalf("a stale edit must be refused: %v", err)
	}
	// With the current version it goes through.
	if _, err = s.ApplyManualTransition(ctx, goal.ID, "W1", "BLOCKED", 2); err != nil {
		t.Fatalf("a current edit must be accepted: %v", err)
	}
}

// A column's total is the number of matching items, not the number drawn: a
// count taken from the rendered page says "50" on a board holding 400.
func TestColumnTotalsAreNotThePageSize(t *testing.T) {
	ctx, s, project, goal := boardFixture(t)
	for i := 0; i < 12; i++ {
		seedItem(t, ctx, s, goal, string(rune('a'+i))+"1", "작업", "BACKLOG", float64(i))
	}
	board, err := s.BoardFor(ctx, project.ID, goal, BoardQuery{PerColumn: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range board.Columns {
		if column.Status != "BACKLOG" {
			continue
		}
		if column.Total != 12 {
			t.Fatalf("the total must count everything that matched: %d", column.Total)
		}
		if len(column.Cards) != 5 {
			t.Fatalf("the page must respect the limit: %d", len(column.Cards))
		}
	}
}

// A card has to say whether it can run and why not, or the reader opens each
// one to find out it was blocked.
func TestCardsCarryTheirBlockersAndTargets(t *testing.T) {
	ctx, s, project, goal := boardFixture(t)
	seedItem(t, ctx, s, goal, "W1", "선행", "BACKLOG", 90)
	seedItem(t, ctx, s, goal, "W2", "후행", "BACKLOG", 80)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO work_item_dependencies(work_item_id,depends_on_id) VALUES('W2','W1')`); err != nil {
		t.Fatal(err)
	}
	board, err := s.BoardFor(ctx, project.ID, goal, BoardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	cards := map[string]BoardCard{}
	for _, column := range board.Columns {
		for _, card := range column.Cards {
			cards[card.Item.ID] = card
		}
	}
	if len(cards["W2"].Blockers) == 0 {
		t.Fatalf("a dependent item must carry its blocker: %+v", cards["W2"])
	}
	if cards["W2"].Runnable() {
		t.Fatal("an item with an unmet dependency is not runnable")
	}
	// Finishing W1 unblocks W2, which is what makes it worth doing first.
	if cards["W1"].Dependents != 1 {
		t.Fatalf("W1 must know something waits on it: %+v", cards["W1"])
	}
	if len(cards["W1"].AllowedTargets) == 0 || cards["W1"].RefusedTargets["DONE"] == "" {
		t.Fatalf("each card must carry what it may and may not become: %+v", cards["W1"])
	}
}

// The board's default order is the order the planner would pick, so the top of
// a column is the next candidate rather than whatever was inserted first.
func TestColumnsAreOrderedByPriority(t *testing.T) {
	ctx, s, project, goal := boardFixture(t)
	seedItem(t, ctx, s, goal, "LOW", "낮음", "BACKLOG", 10)
	seedItem(t, ctx, s, goal, "HIGH", "높음", "BACKLOG", 95)
	board, err := s.BoardFor(ctx, project.ID, goal, BoardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range board.Columns {
		if column.Status == "BACKLOG" && column.Cards[0].Item.ID != "HIGH" {
			t.Fatalf("highest priority first: %+v", column.Cards)
		}
	}
}
