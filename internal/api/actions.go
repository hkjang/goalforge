package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/goalforge/goalforge/internal/app"
	"github.com/goalforge/goalforge/internal/report"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// ActionResult reports what an action did in the user's terms, including what
// is preserved when execution is stopped, so "중지" and "취소" are not two
// buttons with unexplained consequences.
type ActionResult struct {
	Action    string `json:"action"`
	Outcome   string `json:"outcome"`
	Detail    string `json:"detail"`
	JobID     string `json:"job_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Cancelled int64  `json:"cancelled_jobs,omitempty"`
}

// projectAction runs the execution controls the dashboard is allowed to
// trigger. Starting work is always scheduled as a job rather than executed in
// the request: a provider session outlives an HTTP request, and the worker owns
// budget, quota, and preflight checks that a handler must not bypass.
func (s *Server) projectAction(w http.ResponseWriter, r *http.Request) {
	projectID, action := r.PathValue("id"), r.PathValue("action")
	if _, err := s.store.ProjectByID(r.Context(), projectID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch action {
	case "continue":
		job, err := s.queue.ScheduleRecurringJob(r.Context(), store.SchedulerJob{ProjectID: projectID, Type: "CONTINUE",
			RunAt: time.Now().UTC(), IdempotencyKey: "continue:" + projectID})
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ActionResult{Action: action, Outcome: "SCHEDULED", JobID: job.ID,
			Detail: "다음 작업 실행을 예약했습니다. goalforge worker 가 처리합니다."})
	case "pause":
		control, err := s.store.RequestRunControl(r.Context(), projectID, "PAUSE")
		if errors.Is(err, store.ErrNoRunningExecution) {
			writeError(w, http.StatusConflict, "실행 중인 AI 세션이 없어 일시정지할 대상이 없습니다")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ActionResult{Action: action, Outcome: "REQUESTED", RunID: control.RunID,
			Detail: "현재 턴이 끝나면 멈춥니다. 작업 공간과 세션은 보존되며 resume 으로 이어갈 수 있습니다."})
	case "cancel":
		control, err := s.store.RequestRunControl(r.Context(), projectID, "CANCEL")
		if err == nil {
			writeJSON(w, http.StatusOK, ActionResult{Action: action, Outcome: "REQUESTED", RunID: control.RunID,
				Detail: "실행을 중단합니다. 검증을 통과하지 못한 변경은 작업 공간에 남고 작업은 백로그로 돌아갑니다."})
			return
		}
		if !errors.Is(err, store.ErrNoRunningExecution) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		cancelled, err := s.store.CancelProjectJobs(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ActionResult{Action: action, Outcome: "JOBS_CANCELLED", Cancelled: cancelled,
			Detail: "실행 중인 세션이 없어 예약된 작업을 취소했습니다."})
	default:
		writeError(w, http.StatusBadRequest, "지원하지 않는 동작입니다: "+action)
	}
}

// projectPlan answers "what would happen if I started work now" without
// starting it: the item that would be chosen, the model, the expected cost
// against the remaining budget, and every precondition that would refuse it.
func (s *Server) projectPlan(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.ProjectByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	plan, err := app.BuildPlan(r.Context(), s.store, project)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": plan, "runnable": plan.Runnable()})
}

// evidenceBundle serves the handover document. HTML is the default because
// the common use is sending someone a page they can read; JSON is there for
// anything that needs to process it.
func (s *Server) evidenceBundle(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	bundle, err := s.store.BuildEvidenceBundle(r.Context(), projectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project or goal not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, bundle)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="evidence.html"`)
	if err = report.EvidenceHTML(w, bundle); err != nil {
		// The status is already sent, so the only useful thing left is to stop.
		return
	}
}
