package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

type Server struct {
	store *store.Store
	token string
	mux   *http.ServeMux
}

type ProjectSummary struct {
	Project  model.Project        `json:"project"`
	Goal     *model.Goal          `json:"goal,omitempty"`
	Progress float64              `json:"progress_percent"`
	Complete bool                 `json:"complete"`
	Scope    ProgressScope        `json:"progress_scope"`
	Metrics  store.ProjectMetrics `json:"metrics"`
}

// ProgressScope states the baseline a progress percentage was computed over,
// so a client can explain the number instead of only showing it: discarded
// work has left the baseline, and criteria can be unmet independently of it.
type ProgressScope struct {
	TotalWeight     float64 `json:"total_weight"`
	DoneWeight      float64 `json:"done_weight"`
	DiscardedWeight float64 `json:"discarded_weight"`
	TotalItems      int     `json:"total_items"`
	DoneItems       int     `json:"done_items"`
	DiscardedItems  int     `json:"discarded_items"`
	CriteriaMet     bool    `json:"criteria_met"`
}

type ProjectDetail struct {
	ProjectSummary
	WorkItems  []model.WorkItem           `json:"work_items"`
	Sessions   []store.SessionRecord      `json:"sessions"`
	Quotas     []QuotaView                `json:"quota_windows"`
	Jobs       []JobView                  `json:"scheduler_jobs"`
	Budget     *store.ProjectBudget       `json:"budget,omitempty"`
	Daily      *store.DailyUsage          `json:"daily_usage,omitempty"`
	Criteria   []store.CriterionStatus    `json:"criteria"`
	Runs       []store.RunView            `json:"runs"`
	Approvals  []ApprovalView             `json:"pending_approvals"`
	IdeaScores map[string]model.IdeaScore `json:"idea_scores"`
	Series     []store.DailyUsagePoint    `json:"usage_series"`
}

type ApprovalView struct {
	ID, ActionType, Reason string
	RequestedAt            time.Time
	Scope                  store.ApprovalScope
}

type QuotaView struct {
	Provider, AccountID, LimitType, Status, Source, Confidence string
	UsedPercent                                                float64
	QuotaResetAt, ResumeAt                                     *time.Time
}

type JobView struct {
	ID, Type, Status, LastError string
	RunAt                       time.Time
	Attempts                    int
}

func New(s *store.Store, bearerToken string) (*Server, error) {
	if s == nil {
		return nil, errors.New("store is required")
	}
	server := &Server{store: s, token: bearerToken, mux: http.NewServeMux()}
	server.mux.HandleFunc("GET /healthz", server.health)
	server.mux.HandleFunc("GET /metrics", server.metrics)
	server.mux.HandleFunc("GET /api/v1/projects", server.projects)
	server.mux.HandleFunc("GET /api/v1/projects/{id}", server.project)
	server.mux.HandleFunc("GET /api/v1/approvals", server.pendingApprovals)
	server.mux.HandleFunc("GET /api/v1/projects/{id}/runs/{runID}", server.runDetail)
	server.mux.HandleFunc("GET /api/v1/projects/{id}/work", server.workItems)
	server.mux.HandleFunc("GET /api/v1/projects/{id}/work/{workID}", server.workItemDetail)
	server.mux.HandleFunc("POST /api/v1/projects/{id}/work/{workID}/plan", server.updateWorkPlan)
	server.mux.HandleFunc("GET /api/v1/projects/{id}/approvals/{approvalID}", server.approvalDetail)
	server.mux.HandleFunc("POST /api/v1/projects/{id}/actions/{action}", server.projectAction)
	server.mux.HandleFunc("POST /api/v1/projects/{id}/approvals/{approvalID}/approve", server.decideApproval)
	server.mux.HandleFunc("POST /api/v1/projects/{id}/approvals/{approvalID}/reject", server.decideApproval)
	server.mux.HandleFunc("POST /api/v1/projects/{id}/work/{workID}/status/{status}", server.setWorkStatus)
	server.mux.HandleFunc("GET /", server.dashboard)
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		if s.token != "" && (strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics") && r.Header.Get("Authorization") != "Bearer "+s.token {
			writeError(w, http.StatusUnauthorized, "valid bearer token required")
			return
		}
		// Mutations require a custom header so cross-site form posts fail
		// the CORS preflight even when no bearer token is configured.
		if r.Method != http.MethodGet && r.Header.Get("X-Requested-With") != "GoalForge" {
			writeError(w, http.StatusForbidden, "mutations require the X-Requested-With: GoalForge header")
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := make([]ProjectSummary, 0, len(projects))
	for _, project := range projects {
		summary, _, summaryErr := s.summary(r.Context(), project)
		if summaryErr != nil {
			writeError(w, http.StatusInternalServerError, summaryErr.Error())
			return
		}
		result = append(result, summary)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": result})
}

func (s *Server) project(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.ProjectByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	summary, progress, err := s.summary(r.Context(), project)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail := ProjectDetail{ProjectSummary: summary}
	if summary.Goal != nil {
		detail.Criteria = progress.Criteria
		detail.WorkItems, err = s.store.ListWorkItems(r.Context(), summary.Goal.ID)
		if err == nil {
			detail.IdeaScores, err = s.store.IdeaScoresForGoal(r.Context(), summary.Goal.ID)
		}
	}
	if err == nil {
		detail.Runs, err = s.store.ListRecentRuns(r.Context(), project.ID, 15)
	}
	if err == nil {
		detail.Series, err = s.store.DailyUsageSeries(r.Context(), project.ID, 14)
	}
	if err == nil {
		var pending []store.Approval
		pending, err = s.store.ListPendingApprovals(r.Context(), project.ID)
		for _, approval := range pending {
			detail.Approvals = append(detail.Approvals, ApprovalView{ID: approval.ID, ActionType: approval.ActionType, Reason: approval.Reason, RequestedAt: approval.RequestedAt, Scope: approval.Scope})
		}
	}
	if err == nil {
		detail.Sessions, err = s.store.ListSessions(r.Context(), project.ID)
	}
	if err == nil {
		var quotas []store.QuotaWindow
		quotas, err = s.store.ListQuotaWindows(r.Context(), project.Provider)
		for _, quota := range quotas {
			detail.Quotas = append(detail.Quotas, QuotaView{Provider: quota.Provider, AccountID: quota.AccountID, LimitType: quota.LimitType, Status: quota.Status, Source: quota.Source, Confidence: quota.Confidence, UsedPercent: quota.UsedPercent, QuotaResetAt: quota.QuotaResetAt, ResumeAt: quota.ResumeAt})
		}
	}
	if err == nil {
		var jobs []store.SchedulerJob
		jobs, err = s.store.ListSchedulerJobs(r.Context(), project.ID, true)
		for _, job := range jobs {
			detail.Jobs = append(detail.Jobs, JobView{ID: job.ID, Type: job.Type, Status: job.Status, LastError: job.LastError, RunAt: job.RunAt, Attempts: job.Attempts})
		}
	}
	if err == nil {
		if budget, budgetErr := s.store.ProjectBudgetUsage(r.Context(), project.ID); budgetErr == nil {
			detail.Budget = &budget
		} else if !errors.Is(budgetErr, store.ErrNotFound) {
			err = budgetErr
		}
	}
	if err == nil {
		if _, daily, dailyErr := s.store.ProjectDailyUsage(r.Context(), project.ID, time.Now().UTC()); dailyErr == nil {
			detail.Daily = &daily
		} else if !errors.Is(dailyErr, store.ErrNotFound) {
			err = dailyErr
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// RunDetail is one run replayed from authoritative audit records.
type RunDetail struct {
	Run           store.RunView              `json:"run"`
	Prompt        *store.PromptView          `json:"prompt,omitempty"`
	Turns         []store.TurnRecord         `json:"turns"`
	Usage         provider.Usage             `json:"usage"`
	FileChanges   []gitops.FileChange        `json:"file_changes"`
	Verifications []store.VerificationRecord `json:"verifications"`
	Commit        *store.RunCommit           `json:"commit,omitempty"`
	Events        []store.EventLog           `json:"events"`
	Diff          string                     `json:"diff,omitempty"`
	DiffTruncated bool                       `json:"diff_truncated,omitempty"`
	DiffError     string                     `json:"diff_error,omitempty"`
}

// diffLimitBytes caps how much of a patch is sent to a browser; a reviewer
// needs the shape of a change, and an unbounded patch is a denial of service
// against the dashboard rather than a better review.
const diffLimitBytes = 200000

func (s *Server) runDetail(w http.ResponseWriter, r *http.Request) {
	projectID, runID := r.PathValue("id"), r.PathValue("runID")
	project, err := s.store.ProjectByID(r.Context(), projectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail := RunDetail{}
	detail.Run, err = s.store.RunByID(r.Context(), projectID, runID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if prompt, promptErr := s.store.PromptForRun(r.Context(), runID); promptErr == nil {
		detail.Prompt = &prompt
	} else if !errors.Is(promptErr, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, promptErr.Error())
		return
	}
	detail.Turns, err = s.store.ListTurns(r.Context(), runID)
	if err == nil {
		detail.Usage, err = s.store.RunUsage(r.Context(), runID)
	}
	if err == nil {
		detail.FileChanges, err = s.store.ListRunFileChanges(r.Context(), runID)
	}
	if err == nil {
		detail.Verifications, err = s.store.VerificationsForRun(r.Context(), runID)
	}
	if err == nil {
		detail.Events, err = s.store.EventLogsForRun(r.Context(), projectID, runID, 200)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if commit, commitErr := s.store.RunCommitByRun(r.Context(), runID); commitErr == nil {
		detail.Commit = &commit
		// The diff belongs next to the verification evidence: reviewing a
		// change should not mean leaving for a terminal and a git client.
		if diff, truncated, diffErr := gitops.CommitDiff(r.Context(), project.RepositoryPath, commit.CommitSHA, diffLimitBytes); diffErr == nil {
			detail.Diff, detail.DiffTruncated = diff, truncated
		} else {
			detail.DiffError = diffErr.Error()
		}
	} else if !errors.Is(commitErr, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, commitErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) pendingApprovals(w http.ResponseWriter, r *http.Request) {
	approvals, err := s.store.ListAllPendingApprovals(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals})
}

// decideApproval approves or rejects one pending approval. Approvals stay a
// deliberate human action: one decision per request, no bulk endpoint.
func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	projectID, approvalID := r.PathValue("id"), r.PathValue("approvalID")
	var err error
	if strings.HasSuffix(r.URL.Path, "/reject") {
		err = s.store.RejectApproval(r.Context(), projectID, approvalID)
	} else {
		err = s.store.Approve(r.Context(), projectID, approvalID)
	}
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"approval": approvalID, "status": statusForPath(r.URL.Path)})
}

// triageStatuses are the only transitions the dashboard may perform: triage
// decisions on backlog candidates. Execution states stay orchestrator-owned.
var triageStatuses = map[string]bool{"APPROVED": true, "BLOCKED": true, "DISCARDED": true, "BACKLOG": true}

func (s *Server) setWorkStatus(w http.ResponseWriter, r *http.Request) {
	projectID, workID, status := r.PathValue("id"), r.PathValue("workID"), r.PathValue("status")
	if !triageStatuses[status] {
		writeError(w, http.StatusBadRequest, "status must be one of APPROVED, BLOCKED, DISCARDED, BACKLOG")
		return
	}
	goal, err := s.store.CurrentGoal(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusConflict, "no active goal for this project")
		return
	}
	if err = s.store.SetWorkItemStatus(r.Context(), goal.ID, workID, status); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"work_item": workID, "status": status})
}

func statusForPath(path string) string {
	if strings.HasSuffix(path, "/reject") {
		return "REJECTED"
	}
	return "APPROVED"
}

func (s *Server) summary(ctx context.Context, project model.Project) (ProjectSummary, store.ProgressDetail, error) {
	summary := ProjectSummary{Project: project}
	var progress store.ProgressDetail
	goal, err := s.store.CurrentGoal(ctx, project.ID)
	if errors.Is(err, store.ErrNotFound) {
		// A completed project has no ACTIVE goal; show the last goal so the
		// dashboard still explains what was accomplished.
		goal, err = s.store.LatestGoal(ctx, project.ID)
	}
	if err == nil {
		summary.Goal = &goal
		progress, err = s.store.GoalProgressDetail(ctx, goal)
		summary.Progress, summary.Complete = progress.Percent, progress.Complete
		summary.Scope = ProgressScope{TotalWeight: progress.TotalWeight, DoneWeight: progress.DoneWeight,
			DiscardedWeight: progress.DiscardedWeight, TotalItems: progress.TotalItems, DoneItems: progress.DoneItems,
			DiscardedItems: progress.DiscardedItems, CriteriaMet: progress.CriteriaMet}
	} else if errors.Is(err, store.ErrNotFound) {
		err = nil
	}
	if err == nil {
		summary.Metrics, err = s.store.ProjectMetrics(ctx, project.ID)
	}
	return summary, progress, err
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, dashboardHTML)
}
