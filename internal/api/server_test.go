package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func apiFixture(t *testing.T, token string) (*Server, *store.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	project := model.Project{ID: "P-API", Name: "dashboard", RepositoryPath: "/dashboard", DefaultBranch: "main", Provider: "codex", Model: "gpt"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "Ship GoalForge", "objective", "", []model.Criterion{{Type: "build", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateWorkItem(ctx, model.WorkItem{ID: "W-API", GoalID: goal.ID, Type: "IMPLEMENT", Title: "dashboard", ChangeScope: "internal/api/**"}); err != nil {
		t.Fatal(err)
	}
	server, err := New(db, token)
	if err != nil {
		t.Fatal(err)
	}
	return server, db
}

func TestProjectAPIAndDashboard(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/P-API", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var detail ProjectDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Project.Name != "dashboard" || detail.Goal == nil || detail.Goal.Title != "Ship GoalForge" || len(detail.WorkItems) != 1 {
		t.Fatalf("detail=%+v", detail)
	}
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "GoalForge") || response.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestAPIBearerAuthentication(t *testing.T) {
	server, db := apiFixture(t, "secret-token")
	defer db.Close()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
