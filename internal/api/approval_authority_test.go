package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func approvalServer(t *testing.T, token string) (*Server, *store.Store, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := t.Context()
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	approval, err := db.RequestApproval(ctx, project.ID, store.ApprovalProtectedFiles, "touch config")
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(db, token, db)
	if err != nil {
		t.Fatal(err)
	}
	return server, db, approval.ID
}

func approve(t *testing.T, server *Server, approvalID, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/PRJ-1/approvals/"+approvalID+"/approve", nil)
	request.Header.Set("X-Requested-With", "GoalForge")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// The hole: `goalforge serve` on loopback configures no token, so the API
// authenticates nobody. The CSRF header is not a credential — curl sets it in
// one flag. An implementation session runs on the same machine and can reach
// the port, so it could approve its own merge, which the CLI and MCP both
// refuse. The API must not decide approvals when it cannot tell who is asking.
func TestUnauthenticatedAPIRefusesToDecideApprovals(t *testing.T) {
	server, db, approvalID := approvalServer(t, "")
	recorder := approve(t, server, approvalID, "")
	if recorder.Code == http.StatusOK {
		t.Fatal("an API that authenticates nobody must not approve anything")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// The refusal has to say what to do instead, or it is just a wall.
	body := recorder.Body.String()
	if !strings.Contains(body, "GOALFORGE_API_TOKEN") || !strings.Contains(body, "approval approve") {
		t.Fatalf("the refusal must name both remedies: %s", body)
	}
	approval, err := db.ApprovalByID(t.Context(), "PRJ-1", approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != "PENDING" {
		t.Fatalf("the approval must be untouched: %s", approval.Status)
	}
}

// Reading is still fine without a token: the loopback dashboard is how people
// watch a run, and breaking that would not make anything safer.
func TestUnauthenticatedAPIStillServesReads(t *testing.T) {
	server, _, _ := approvalServer(t, "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("reads must keep working: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// With a token configured the caller proved they hold an operator secret that
// sessions are never given, so the decision is attributable and allowed.
func TestAuthenticatedOperatorMayDecideApprovals(t *testing.T) {
	server, db, approvalID := approvalServer(t, "operator-secret")
	recorder := approve(t, server, approvalID, "operator-secret")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	approval, err := db.ApprovalByID(t.Context(), "PRJ-1", approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != "APPROVED" {
		t.Fatalf("status=%s", approval.Status)
	}
}

// A wrong token is refused by the existing bearer check; this pins that the
// approval path did not somehow become reachable around it.
func TestWrongTokenCannotApprove(t *testing.T) {
	server, _, approvalID := approvalServer(t, "operator-secret")
	if recorder := approve(t, server, approvalID, "guessed"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", recorder.Code)
	}
}

// A boundary the CLI enforces and the API does not is not a boundary, and the
// session reaches both. These are the operations `goalforge` itself refuses to
// an implementation session.
func TestPrivilegedRoutesRefuseAnUnidentifiedCaller(t *testing.T) {
	server, db, _ := approvalServer(t, "")
	for name, tc := range map[string]struct{ path, body string }{
		// Redefining the goal is how an unfinished job is made to look
		// finished: easier criteria, same claim of completion.
		"goal": {"/api/v1/projects/PRJ-1/goal",
			`{"title":"easier","objective":"o","reason":"r","criteria":[{"type":"build","expected_value":"true"}]}`},
		// A run that can raise its own ceiling has no ceiling.
		"policy": {"/api/v1/projects/PRJ-1/policy", `{"token_limit":999999999}`},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("X-Requested-With", "GoalForge")
			request.Header.Set("Content-Type", "application/json")
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	// Nothing was changed by the refused requests.
	if _, err := db.CurrentGoal(t.Context(), "PRJ-1"); err == nil {
		t.Fatal("the refused request must not have created a goal")
	}
}

// The operations the CLI leaves open to a session stay open here, or the guard
// would be a different boundary wearing the same name.
func TestUnprivilegedRoutesStayOpen(t *testing.T) {
	server, _, _ := approvalServer(t, "")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/PRJ-1/decisions",
		strings.NewReader(`{"title":"t","decision":"d"}`))
	request.Header.Set("X-Requested-With", "GoalForge")
	request.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code == http.StatusForbidden {
		t.Fatalf("recording a decision is not a privileged operation: %s", recorder.Body.String())
	}
}
