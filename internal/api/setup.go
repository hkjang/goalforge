package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/diagnostics"
	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// doctor runs the same environment checks as the CLI so the setup flow can
// diagnose a repository before a project is created, rather than leaving the
// first failure to a run.
func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	options := diagnostics.Options{}
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if provider := strings.TrimSpace(r.URL.Query().Get("provider")); provider != "" {
		if !diagnostics.IsSupported(provider) {
			writeError(w, http.StatusBadRequest, "지원하지 않는 제공자입니다: "+provider)
			return
		}
		options.Providers = []string{provider}
		options.StrictCLI = true
	}
	if repo != "" {
		absolute, err := filepath.Abs(repo)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		switch project, projectErr := s.store.ProjectByPath(r.Context(), absolute); {
		case projectErr == nil:
			options.ProjectLevel = diagnostics.LevelOK
			options.ProjectNote = project.Name + " 프로젝트가 이미 이 경로에 등록되어 있습니다"
		case errors.Is(projectErr, store.ErrNotFound):
			if _, statErr := os.Stat(filepath.Join(absolute, ".git")); statErr != nil {
				options.ProjectLevel = diagnostics.LevelFail
				options.ProjectNote = absolute + " 는 Git 저장소가 아닙니다. 먼저 git init 으로 저장소를 만드세요."
			} else {
				options.ProjectLevel = diagnostics.LevelOK
				options.ProjectNote = absolute + " 는 Git 저장소이며 아직 GoalForge 에 등록되지 않았습니다"
			}
		default:
			writeError(w, http.StatusInternalServerError, projectErr.Error())
			return
		}
	}
	report := diagnostics.Run(r.Context(), options)
	if projectID := strings.TrimSpace(r.URL.Query().Get("project")); projectID != "" {
		readiness, err := s.store.ReadinessInput(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, check := range diagnostics.CheckReadiness(readiness) {
			report.Checks = append(report.Checks, check)
			if check.Level == diagnostics.LevelFail {
				report.Failed++
			}
		}
	}
	writeJSON(w, http.StatusOK, report)
}

type createProjectRequest struct {
	Name           string `json:"name"`
	RepositoryPath string `json:"repository_path"`
	DefaultBranch  string `json:"default_branch"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	FallbackModel  string `json:"fallback_model"`
	Worktrees      bool   `json:"worktrees"`
	AutoCommit     bool   `json:"auto_commit"`
}

// createProject registers a repository from the setup flow. It applies the
// same checks as `project init`: a supported provider and an actual Git
// repository, with the default branch read from the repository when omitted.
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var request createProjectRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(request.Name) == "" {
		writeError(w, http.StatusBadRequest, "프로젝트 이름이 필요합니다")
		return
	}
	if !diagnostics.IsSupported(request.Provider) {
		writeError(w, http.StatusBadRequest, "제공자는 "+strings.Join(diagnostics.Supported, ", ")+" 중 하나여야 합니다")
		return
	}
	absolute, err := filepath.Abs(request.RepositoryPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err = os.Stat(filepath.Join(absolute, ".git")); err != nil {
		writeError(w, http.StatusBadRequest, absolute+" 는 Git 저장소가 아닙니다")
		return
	}
	branch := strings.TrimSpace(request.DefaultBranch)
	if branch == "" {
		output, branchErr := exec.CommandContext(r.Context(), "git", "-C", absolute, "branch", "--show-current").Output()
		if branchErr != nil {
			writeError(w, http.StatusBadRequest, "Git 브랜치를 읽을 수 없습니다: "+branchErr.Error())
			return
		}
		if branch = strings.TrimSpace(string(output)); branch == "" {
			branch = "main"
		}
	}
	project := model.Project{Name: request.Name, RepositoryPath: absolute, DefaultBranch: branch,
		Provider: request.Provider, Model: request.Model, FallbackModel: request.FallbackModel,
		WorktreeEnabled: request.Worktrees, AutoCommitEnabled: request.AutoCommit}
	if err = s.store.CreateProject(r.Context(), project); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	created, err := s.store.ProjectByPath(r.Context(), absolute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": created})
}

type setGoalRequest struct {
	Title     string `json:"title"`
	Objective string `json:"objective"`
	Reason    string `json:"reason"`
	Criteria  []struct {
		Type          string `json:"type"`
		ExpectedValue string `json:"expected_value"`
		// RequiredKind demands a kind of check (journey, integration, ...);
		// empty accepts any gate, which is how goals were recorded before.
		RequiredKind string `json:"required_kind"`
	} `json:"criteria"`
}

// setGoal records a goal version. Criteria are required: without them nothing
// can judge the goal complete, and a goal that cannot complete is not a goal.
func (s *Server) setGoal(w http.ResponseWriter, r *http.Request) {
	var request setGoalRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Objective) == "" {
		writeError(w, http.StatusBadRequest, "목표 제목과 내용이 필요합니다")
		return
	}
	if len(request.Criteria) == 0 {
		writeError(w, http.StatusBadRequest, "완료 조건이 최소 1개 필요합니다")
		return
	}
	criteria := make([]model.Criterion, 0, len(request.Criteria))
	for _, c := range request.Criteria {
		if strings.TrimSpace(c.Type) == "" || strings.TrimSpace(c.ExpectedValue) == "" {
			writeError(w, http.StatusBadRequest, "완료 조건은 이름과 기준값이 모두 필요합니다")
			return
		}
		criteria = append(criteria, model.Criterion{Type: c.Type, ExpectedValue: c.ExpectedValue, RequiredKind: c.RequiredKind})
	}
	goal, err := s.store.SetGoal(r.Context(), r.PathValue("id"), request.Title, request.Objective, request.Reason, criteria)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"goal": goal})
}

type policyRequest struct {
	TokenLimit        *int64   `json:"token_limit"`
	CostLimitUSD      *float64 `json:"cost_limit_usd"`
	DailyRunLimit     *int64   `json:"daily_run_limit"`
	DailyTokenLimit   *int64   `json:"daily_token_limit"`
	DailyCostLimitUSD *float64 `json:"daily_cost_limit_usd"`
	TurnTimeout       string   `json:"turn_timeout"`
	RunTimeout        string   `json:"run_timeout"`
	WIPLimit          *int     `json:"wip_limit"`
}

// setPolicy configures the budget and timeouts that bound every later run.
// Omitted fields keep their current value, and everything is validated before
// anything is written: a request rejected halfway through would otherwise
// leave the project with a budget it was never asked for.
func (s *Server) setPolicy(w http.ResponseWriter, r *http.Request) {
	var request policyRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	projectID := r.PathValue("id")
	current, err := s.store.ProjectBudgetConfig(r.Context(), projectID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	budget := store.ProjectBudget{TokenLimit: current.TokenLimit, CostLimitUSD: current.CostLimitUSD,
		DailyRunLimit: current.DailyRunLimit, DailyTokenLimit: current.DailyTokenLimit, DailyCostLimitUSD: current.DailyCostLimitUSD}
	if request.TokenLimit != nil {
		budget.TokenLimit = *request.TokenLimit
	}
	if request.CostLimitUSD != nil {
		budget.CostLimitUSD = *request.CostLimitUSD
	}
	if request.DailyRunLimit != nil {
		budget.DailyRunLimit = *request.DailyRunLimit
	}
	if request.DailyTokenLimit != nil {
		budget.DailyTokenLimit = *request.DailyTokenLimit
	}
	if request.DailyCostLimitUSD != nil {
		budget.DailyCostLimitUSD = *request.DailyCostLimitUSD
	}
	if budget.TokenLimit < 0 || budget.CostLimitUSD < 0 || budget.DailyRunLimit < 0 || budget.DailyTokenLimit < 0 || budget.DailyCostLimitUSD < 0 {
		writeError(w, http.StatusBadRequest, "예산 한도는 음수일 수 없습니다")
		return
	}
	if request.WIPLimit != nil && (*request.WIPLimit < 1 || *request.WIPLimit > 8) {
		writeError(w, http.StatusBadRequest, "동시 구현 한도는 1에서 8 사이여야 합니다")
		return
	}
	setTimeouts := request.TurnTimeout != "" || request.RunTimeout != ""
	var policy store.RuntimePolicy
	if setTimeouts {
		turn, parseErr := parseDuration(request.TurnTimeout, store.DefaultRuntimePolicy().TurnTimeout)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "턴 제한 시간: "+parseErr.Error())
			return
		}
		run, parseErr := parseDuration(request.RunTimeout, store.DefaultRuntimePolicy().RunTimeout)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "실행 제한 시간: "+parseErr.Error())
			return
		}
		if turn <= 0 || run <= 0 || turn > run {
			writeError(w, http.StatusBadRequest, "제한 시간은 양수여야 하고 턴 제한이 실행 제한보다 길 수 없습니다")
			return
		}
		policy = store.RuntimePolicy{TurnTimeout: turn, RunTimeout: run}
	}
	if err = s.store.SetProjectBudget(r.Context(), projectID, budget.TokenLimit, budget.CostLimitUSD); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err = s.store.SetDailyLimits(r.Context(), projectID, budget.DailyRunLimit, budget.DailyTokenLimit, budget.DailyCostLimitUSD); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if setTimeouts {
		if err = s.store.SetRuntimePolicy(r.Context(), projectID, policy); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if request.WIPLimit != nil {
		if err = s.store.SetWIPLimit(r.Context(), projectID, *request.WIPLimit); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": projectID, "status": "configured", "budget": budget})
}

func parseDuration(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(into); err != nil {
		return errors.New("요청 본문을 읽을 수 없습니다: " + err.Error())
	}
	return nil
}

// activityReport summarizes a window of automated work for someone returning
// to it, rather than making them reconstruct it from run logs.
func (s *Server) activityReport(w http.ResponseWriter, r *http.Request) {
	window := 24 * time.Hour
	if value := strings.TrimSpace(r.URL.Query().Get("since")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "since 는 5m, 24h 같은 양수 기간이어야 합니다")
			return
		}
		window = parsed
	}
	report, err := s.store.Activity(r.Context(), time.Now().UTC().Add(-window))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) listDecisions(w http.ResponseWriter, r *http.Request) {
	includeSuperseded := r.URL.Query().Get("all") == "true"
	decisions, err := s.store.ListDecisions(r.Context(), r.PathValue("id"), includeSuperseded)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if decisions == nil {
		decisions = []store.DesignDecision{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": decisions})
}

type decisionRequest struct {
	Title        string `json:"title"`
	Context      string `json:"context"`
	Decision     string `json:"decision"`
	Alternatives string `json:"alternatives"`
	Consequences string `json:"consequences"`
	WorkItemID   string `json:"work_item_id"`
	Supersedes   string `json:"supersedes"`
}

// recordDecision stores why a structure was chosen. Decisions are never
// deleted — one is superseded by another — because the record of what was
// rejected is what stops it being proposed again.
func (s *Server) recordDecision(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	project, err := s.store.ProjectByID(r.Context(), projectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var request decisionRequest
	if decodeErr := decodeBody(w, r, &request); decodeErr != nil {
		writeError(w, http.StatusBadRequest, decodeErr.Error())
		return
	}
	if strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Decision) == "" {
		writeError(w, http.StatusBadRequest, "제목과 결정 내용이 필요합니다")
		return
	}
	goalID := ""
	if goal, goalErr := s.goalForProject(r, projectID); goalErr == nil {
		goalID = goal.ID
	} else if !errors.Is(goalErr, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, goalErr.Error())
		return
	}
	baseCommit, _ := gitops.HeadCommit(r.Context(), project.RepositoryPath, project.DefaultBranch)
	decision, err := s.store.RecordDecision(r.Context(), store.DesignDecision{ProjectID: projectID, GoalID: goalID,
		WorkItem: request.WorkItemID, Title: request.Title, Context: request.Context, Decision: request.Decision,
		Alternatives: request.Alternatives, Consequences: request.Consequences, BaseCommit: baseCommit})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Supersedes != "" {
		if err = s.store.SupersedeDecision(r.Context(), projectID, request.Supersedes, decision.ID); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"decision": decision})
}
