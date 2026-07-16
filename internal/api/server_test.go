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

func TestApprovalInboxAndDecisions(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	ctx := context.Background()
	first, err := db.RequestApproval(ctx, "P-API", store.ApprovalMergeBranch, "merge test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.RequestApproval(ctx, "P-API", store.ApprovalPublishBranch, "publish test")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/approvals", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	var inbox struct {
		Approvals []store.PendingApproval `json:"approvals"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &inbox); err != nil {
		t.Fatal(err)
	}
	if len(inbox.Approvals) != 2 || inbox.Approvals[0].ProjectName != "dashboard" {
		t.Fatalf("inbox=%+v", inbox)
	}
	// Mutations without the CSRF header are rejected even without a token.
	request = httptest.NewRequest(http.MethodPost, "/api/v1/projects/P-API/approvals/"+first.ID+"/approve", nil)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF header must be rejected: %d", response.Code)
	}
	post := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Header.Set("X-Requested-With", "GoalForge")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if response := post("/api/v1/projects/P-API/approvals/" + first.ID + "/approve"); response.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", response.Code, response.Body.String())
	}
	if response := post("/api/v1/projects/P-API/approvals/" + second.ID + "/reject"); response.Code != http.StatusOK {
		t.Fatalf("reject status=%d body=%s", response.Code, response.Body.String())
	}
	// Double decisions conflict; approved consumes, rejected never does.
	if response := post("/api/v1/projects/P-API/approvals/" + first.ID + "/approve"); response.Code != http.StatusConflict {
		t.Fatalf("second approve must conflict: %d", response.Code)
	}
	approved, err := db.ConsumeApproval(ctx, "P-API", store.ApprovalMergeBranch, "run-1")
	if err != nil || !approved {
		t.Fatalf("approved=%t err=%v", approved, err)
	}
	rejected, err := db.ConsumeApproval(ctx, "P-API", store.ApprovalPublishBranch, "run-2")
	if err != nil || rejected {
		t.Fatalf("rejected approval must not be consumable: %t err=%v", rejected, err)
	}
}

func TestMetricsEndpointServesPrometheusFormatBehindBearer(t *testing.T) {
	server, db := apiFixture(t, "secret")
	defer db.Close()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated scrape must be rejected: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("content type %q", response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	for _, expected := range []string{
		"# HELP goalforge_runs_total",
		"# TYPE goalforge_runs_total counter",
		`goalforge_runs_total{project="dashboard",state="READY"} 0`,
		`goalforge_goal_progress_percent{project="dashboard"`,
		"goalforge_cost_usd_total",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in metrics:\n%s", expected, body)
		}
	}
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
