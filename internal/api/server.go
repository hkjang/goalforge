package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/model"
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
	Metrics  store.ProjectMetrics `json:"metrics"`
}

type ProjectDetail struct {
	ProjectSummary
	WorkItems []model.WorkItem        `json:"work_items"`
	Sessions  []store.SessionRecord   `json:"sessions"`
	Quotas    []QuotaView             `json:"quota_windows"`
	Jobs      []JobView               `json:"scheduler_jobs"`
	Budget    *store.ProjectBudget    `json:"budget,omitempty"`
	Daily     *store.DailyUsage       `json:"daily_usage,omitempty"`
	Criteria  []store.CriterionStatus `json:"criteria"`
	Runs      []store.RunView         `json:"runs"`
	Approvals []ApprovalView          `json:"pending_approvals"`
}

type ApprovalView struct {
	ID, ActionType, Reason string
	RequestedAt            time.Time
}

type QuotaView struct {
	Provider, AccountID, LimitType, Status, Source, Confidence string
	UsedPercent                                                float64
	QuotaResetAt, ResumeAt                                     *time.Time
}

type JobView struct {
	ID, Type, Status string
	RunAt            time.Time
	Attempts         int
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
		summary, summaryErr := s.summary(r.Context(), project)
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
	summary, err := s.summary(r.Context(), project)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail := ProjectDetail{ProjectSummary: summary}
	if summary.Goal != nil {
		detail.WorkItems, err = s.store.ListWorkItems(r.Context(), summary.Goal.ID)
		if err == nil {
			detail.Criteria, err = s.store.CriteriaStatus(r.Context(), *summary.Goal)
		}
	}
	if err == nil {
		detail.Runs, err = s.store.ListRecentRuns(r.Context(), project.ID, 15)
	}
	if err == nil {
		var pending []store.Approval
		pending, err = s.store.ListPendingApprovals(r.Context(), project.ID)
		for _, approval := range pending {
			detail.Approvals = append(detail.Approvals, ApprovalView{ID: approval.ID, ActionType: approval.ActionType, Reason: approval.Reason, RequestedAt: approval.RequestedAt})
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
			detail.Jobs = append(detail.Jobs, JobView{ID: job.ID, Type: job.Type, Status: job.Status, RunAt: job.RunAt, Attempts: job.Attempts})
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

func (s *Server) summary(ctx context.Context, project model.Project) (ProjectSummary, error) {
	summary := ProjectSummary{Project: project}
	goal, err := s.store.CurrentGoal(ctx, project.ID)
	if errors.Is(err, store.ErrNotFound) {
		// A completed project has no ACTIVE goal; show the last goal so the
		// dashboard still explains what was accomplished.
		goal, err = s.store.LatestGoal(ctx, project.ID)
	}
	if err == nil {
		summary.Goal = &goal
		summary.Progress, summary.Complete, err = s.store.GoalProgress(ctx, goal)
	} else if errors.Is(err, store.ErrNotFound) {
		err = nil
	}
	if err == nil {
		summary.Metrics, err = s.store.ProjectMetrics(ctx, project.ID)
	}
	return summary, err
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
