package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// recordingQueue stands in for a queue on another machine: if the handler
// enqueues into the store instead of the injected queue, nothing arrives here.
type recordingQueue struct{ jobs []store.SchedulerJob }

func (q *recordingQueue) ScheduleRecurringJob(_ context.Context, job store.SchedulerJob) (store.SchedulerJob, error) {
	job.ID = "JOB-FROM-QUEUE"
	job.Status = "PENDING"
	q.jobs = append(q.jobs, job)
	return job, nil
}

// The dashboard's "continue" button must enqueue into the queue the worker
// drains. When PostgreSQL is configured the worker drains PostgreSQL, so an
// enqueue that went to the local SQLite store would be a button that reports
// success and never runs.
func TestContinueActionEnqueuesIntoTheInjectedQueue(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "codex"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	queue := &recordingQueue{}
	server, err := New(db, "", queue)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/PRJ-1/actions/continue", nil)
	request.Header.Set("X-Requested-With", "GoalForge")
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(queue.jobs) != 1 || queue.jobs[0].ProjectID != "PRJ-1" {
		t.Fatalf("the action must enqueue into the injected queue: %+v", queue.jobs)
	}
	// Nothing may have been written to the local queue instead.
	if _, err = db.ClaimDueJob(ctx, queue.jobs[0].RunAt.Add(time.Minute), "probe", time.Minute); err != store.ErrNotFound {
		t.Fatalf("the job must not also land in the local store: %v", err)
	}
}

// A server built without a queue would silently fall back to some default; it
// refuses instead, because the fallback is the failure being prevented.
func TestServerRefusesToStartWithoutAQueue(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = New(db, "", nil); err == nil {
		t.Fatal("a server with no queue must refuse to start")
	}
}
