package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func boardServer(t *testing.T) (*Server, *store.Store, model.Goal) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := t.Context()
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: t.TempDir(),
		DefaultBranch: "main", Provider: "codex", WIPLimit: 1}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "G", "o", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(db, "operator-secret", db)
	if err != nil {
		t.Fatal(err)
	}
	return server, db, goal
}

func seedBoardItem(t *testing.T, db *store.Store, goalID, id, title, status string, priority float64) {
	t.Helper()
	if err := db.SeedBoardItem(t.Context(), goalID, id, title, status, priority); err != nil {
		t.Fatal(err)
	}
}

// Every column the board draws must name itself, or the screen has to guess
// the heading from the order the server happened to send them in.
func TestBoardColumnsCarryTheirStatusAndLabel(t *testing.T) {
	server, db, goal := boardServer(t)
	seedBoardItem(t, db, goal.ID, "W1", "작업", "BACKLOG", 90)
	var board store.Board
	if response := get(t, server, "/api/v1/projects/PRJ-1/board", &board); response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(board.Columns) != len(store.BoardStatuses) {
		t.Fatalf("every column is present even when empty: %d", len(board.Columns))
	}
	for _, column := range board.Columns {
		if column.Status == "" || column.Label == "" {
			t.Fatalf("a column must name itself: %+v", column)
		}
	}
}

// The board offers only what the server accepts, and the refusal it shows is
// the refusal the server would give.
func TestBoardRefusesTheSameMovesTheServerDoes(t *testing.T) {
	server, db, goal := boardServer(t)
	seedBoardItem(t, db, goal.ID, "W1", "작업", "BACKLOG", 90)
	body := `{"status":"DONE","version":1}`
	response := mutate(t, server, http.MethodPost, "/api/v1/projects/PRJ-1/work/W1/transition", body)
	if response.Code != http.StatusForbidden {
		t.Fatalf("a person must not mark work done: status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "검증") {
		t.Fatalf("the refusal must say why: %s", response.Body.String())
	}
	// And the allowed one goes through.
	response = mutate(t, server, http.MethodPost, "/api/v1/projects/PRJ-1/work/W1/transition",
		`{"status":"APPROVED","version":1}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// A drag made against a board loaded before somebody else's change must be
// refused rather than overwriting it. 409, because nothing was malformed —
// the world moved, and the remedy is to reload.
func TestStaleDragIsAConflictNotABadRequest(t *testing.T) {
	server, db, goal := boardServer(t)
	seedBoardItem(t, db, goal.ID, "W1", "작업", "BACKLOG", 90)
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects/PRJ-1/work/W1/transition",
		`{"status":"APPROVED","version":1}`); response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	response := mutate(t, server, http.MethodPost, "/api/v1/projects/PRJ-1/work/W1/transition",
		`{"status":"BLOCKED","version":1}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("a stale drag is a conflict: status=%d body=%s", response.Code, response.Body.String())
	}
}

// Filtering narrows what is shown and says so, so the totals are read as
// "matching" rather than "all".
func TestBoardFiltersAndSaysItDid(t *testing.T) {
	server, db, goal := boardServer(t)
	seedBoardItem(t, db, goal.ID, "W1", "결제 재시도", "BACKLOG", 90)
	seedBoardItem(t, db, goal.ID, "W2", "문서 정리", "BACKLOG", 50)
	var board store.Board
	get(t, server, "/api/v1/projects/PRJ-1/board?q=결제", &board)
	if !board.Filtered {
		t.Fatal("a narrowed board must say it was narrowed")
	}
	total := 0
	for _, column := range board.Columns {
		total += column.Total
	}
	if total != 1 {
		t.Fatalf("only the matching item: %d", total)
	}
}

// A project with no goal is a normal state for a fresh install, not an error
// that leaves the screen blank with a stack trace.
func TestBoardWithoutAGoalIsEmptyNotAnError(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateProject(t.Context(), model.Project{ID: "PRJ-1", Name: "demo",
		RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	server, err := New(db, "operator-secret", db)
	if err != nil {
		t.Fatal(err)
	}
	var board store.Board
	if response := get(t, server, "/api/v1/projects/PRJ-1/board", &board); response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if len(board.Columns) != 0 {
		t.Fatalf("columns=%+v", board.Columns)
	}
}
