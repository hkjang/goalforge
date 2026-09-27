package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func mutate(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader = strings.NewReader(body)
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("X-Requested-With", "GoalForge")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func get(t *testing.T, server *Server, path string, into any) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if into != nil && recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), into); err != nil {
			t.Fatalf("decode %s: %v body=%s", path, err, recorder.Body.String())
		}
	}
	return recorder
}

// The backlog is reachable in full, with the search and status filters the
// four-per-column kanban had no answer for.
func TestWorkItemSearchAndFilter(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	ctx := context.Background()
	goal := goalID(t, db)
	for _, item := range []model.WorkItem{
		{ID: "W-EXPORT", GoalID: goal, Type: "IMPLEMENT", Title: "Add CSV exporter", ChangeScope: "internal/export/**"},
		{ID: "W-DOCS", GoalID: goal, Type: "DOCS", Title: "Document the API", Status: "DISCARDED"},
	} {
		if _, err := db.CreateWorkItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	var all struct {
		WorkItems []model.WorkItem `json:"work_items"`
	}
	get(t, server, "/api/v1/projects/P-API/work", &all)
	if len(all.WorkItems) != 3 {
		t.Fatalf("expected the whole backlog, got %d", len(all.WorkItems))
	}
	var filtered struct {
		WorkItems []model.WorkItem `json:"work_items"`
	}
	get(t, server, "/api/v1/projects/P-API/work?status=DISCARDED", &filtered)
	if len(filtered.WorkItems) != 1 || filtered.WorkItems[0].ID != "W-DOCS" {
		t.Fatalf("status filter: %+v", filtered.WorkItems)
	}
	var searched struct {
		WorkItems []model.WorkItem `json:"work_items"`
	}
	get(t, server, "/api/v1/projects/P-API/work?q=exporter", &searched)
	if len(searched.WorkItems) != 1 || searched.WorkItems[0].ID != "W-EXPORT" {
		t.Fatalf("search: %+v", searched.WorkItems)
	}
}

// A work item's detail names what is blocking it instead of leaving the user
// to infer it from a status code, and editing the plan cannot change status.
func TestWorkItemDetailAndPlanEditing(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	ctx := context.Background()
	goal := goalID(t, db)
	if _, err := db.CreateWorkItem(ctx, model.WorkItem{ID: "W-DEP", GoalID: goal, Type: "IMPLEMENT", Title: "migration"}); err != nil {
		t.Fatal(err)
	}
	response := mutate(t, server, http.MethodPost, "/api/v1/projects/P-API/work/W-API/plan",
		`{"objective":"세션 저장소를 교체한다","acceptance":"재시작 후 세션이 살아있다","dependencies":["W-DEP"],"estimated_tokens":12000,"risk":"high"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("plan update status=%d body=%s", response.Code, response.Body.String())
	}
	var detail WorkItemDetailView
	get(t, server, "/api/v1/projects/P-API/work/W-API", &detail)
	if detail.Item.Objective == "" || detail.Item.Acceptance == "" || detail.Item.Risk != "high" || detail.Item.EstimatedTokens != 12000 {
		t.Fatalf("plan not persisted: %+v", detail.Item)
	}
	if detail.Item.Status != "BACKLOG" {
		t.Fatalf("a planning edit must not move the lifecycle: %s", detail.Item.Status)
	}
	if detail.Actionable {
		t.Fatalf("an item with an unfinished dependency is not actionable: %+v", detail.Blockers)
	}
	if len(detail.Blockers) == 0 || detail.Blockers[0].Kind != "DEPENDENCY" || !strings.Contains(detail.Blockers[0].Detail, "W-DEP") {
		t.Fatalf("blockers must name the dependency: %+v", detail.Blockers)
	}
	if detail.EstimateSource != "manual" {
		t.Fatalf("estimate source=%s", detail.EstimateSource)
	}
	// A dependency has to exist, and an item cannot depend on itself.
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects/P-API/work/W-API/plan", `{"dependencies":["W-API"]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("self dependency must be rejected: %d", response.Code)
	}
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects/P-API/work/W-API/plan", `{"dependencies":["W-GHOST"]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown dependency must be rejected: %d", response.Code)
	}
}

// An approval detail states the grounds for the decision and flags a change
// that moved after the review.
func TestApprovalDetailShowsGroundsAndStaleness(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	ctx := context.Background()
	if err := db.StartRun(ctx, store.RunRecord{ID: "R-APR", ProjectID: "P-API", WorkItemID: "W-API", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordRunCommit(ctx, store.RunCommit{RunID: "R-APR", ProjectID: "P-API", GoalID: goalID(t, db), WorkItemID: "W-API",
		CommitSHA: "1111111111111111", Branch: "goalforge/W-API", FilesCommitted: 2}); err != nil {
		t.Fatal(err)
	}
	approval, err := db.RequestScopedApproval(ctx, "P-API", store.ApprovalMergeBranch, "merge it",
		store.ApprovalScope{WorkItemID: "W-API", SourceBranch: "goalforge/W-API", TargetRef: "main", CommitSHA: "1111111111111111", FilesChanged: 2})
	if err != nil {
		t.Fatal(err)
	}
	var view ApprovalDetailView
	get(t, server, "/api/v1/projects/P-API/approvals/"+approval.ID, &view)
	if view.Approval.Scope.CommitSHA != "1111111111111111" || view.Stale {
		t.Fatalf("fresh approval: %+v", view)
	}
	if !strings.Contains(view.Rollback, "rollback") {
		t.Fatalf("an approval must say how to undo it: %q", view.Rollback)
	}
	// A second verified commit for the same work item makes the review stale.
	if err := db.FinishRun(ctx, "R-APR", "COMPLETED", "READY"); err != nil {
		t.Fatal(err)
	}
	if err := db.StartRun(ctx, store.RunRecord{ID: "R-APR2", ProjectID: "P-API", WorkItemID: "W-API", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordRunCommit(ctx, store.RunCommit{RunID: "R-APR2", ProjectID: "P-API", GoalID: goalID(t, db), WorkItemID: "W-API",
		CommitSHA: "2222222222222222", Branch: "goalforge/W-API", FilesCommitted: 3}); err != nil {
		t.Fatal(err)
	}
	view = ApprovalDetailView{}
	get(t, server, "/api/v1/projects/P-API/approvals/"+approval.ID, &view)
	if !view.Stale || !strings.Contains(view.StaleReason, "222222222222") {
		t.Fatalf("a commit made after review must be flagged: %+v", view)
	}
}

// Execution control from the dashboard schedules work for the worker and
// explains what stopping preserves.
func TestProjectActions(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	response := mutate(t, server, http.MethodPost, "/api/v1/projects/P-API/actions/continue", "")
	if response.Code != http.StatusOK {
		t.Fatalf("continue status=%d body=%s", response.Code, response.Body.String())
	}
	var result ActionResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "SCHEDULED" || result.JobID == "" || result.Detail == "" {
		t.Fatalf("result=%+v", result)
	}
	// Nothing is running, so pausing is refused with a reason rather than
	// silently doing nothing.
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects/P-API/actions/pause", ""); response.Code != http.StatusConflict {
		t.Fatalf("pause status=%d body=%s", response.Code, response.Body.String())
	}
	// Cancel falls back to cancelling the job that was just scheduled.
	response = mutate(t, server, http.MethodPost, "/api/v1/projects/P-API/actions/cancel", "")
	if response.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", response.Code, response.Body.String())
	}
	result = ActionResult{}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "JOBS_CANCELLED" || result.Cancelled != 1 {
		t.Fatalf("result=%+v", result)
	}
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects/P-API/actions/deploy", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown action status=%d", response.Code)
	}
}

// The live view fetches only what it has not seen; re-reading the whole run on
// a timer was the old behaviour and does not scale past a long run.
func TestRunEventsAreIncremental(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	ctx := context.Background()
	if err := db.StartRun(ctx, store.RunRecord{ID: "R-EV", ProjectID: "P-API", WorkItemID: "W-API", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := db.RecordProviderEvent(ctx, "P-API", provider.Event{RunID: "R-EV", Type: provider.EventCompleted,
			TurnID: "t" + strconv.Itoa(i), Raw: json.RawMessage(`{"n":` + strconv.Itoa(i) + `}`)}); err != nil {
			t.Fatal(err)
		}
	}
	var first struct {
		Events []store.EventLog `json:"events"`
		LastID int64            `json:"last_id"`
		State  string           `json:"state"`
	}
	get(t, server, "/api/v1/projects/P-API/runs/R-EV/events", &first)
	if len(first.Events) != 3 || first.LastID == 0 || first.State == "" {
		t.Fatalf("first page=%+v", first)
	}
	var second struct {
		Events []store.EventLog `json:"events"`
		LastID int64            `json:"last_id"`
	}
	get(t, server, "/api/v1/projects/P-API/runs/R-EV/events?after="+strconv.FormatInt(first.LastID, 10), &second)
	if len(second.Events) != 0 {
		t.Fatalf("nothing new must return nothing: %+v", second.Events)
	}
	if response := get(t, server, "/api/v1/projects/P-API/runs/R-GHOST/events", nil); response.Code != http.StatusNotFound {
		t.Fatalf("unknown run status=%d", response.Code)
	}
}

// A finished run's stream ends instead of holding a connection open forever.
func TestRunStreamEndsOnTerminalState(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	ctx := context.Background()
	if err := db.StartRun(ctx, store.RunRecord{ID: "R-STREAM", ProjectID: "P-API", WorkItemID: "W-API", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, "R-STREAM", "COMPLETED", "READY"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/P-API/runs/R-STREAM/stream", nil)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(recorder, request)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not close for a finished run")
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: done") || !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("body=%q content-type=%q", body, recorder.Header().Get("Content-Type"))
	}
}
