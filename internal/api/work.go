package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// WorkItemDetailView is one work item with the context needed to act on it:
// the specification, what is blocking it, and every run that attempted it.
type WorkItemDetailView struct {
	Item           model.WorkItem          `json:"item"`
	Dependencies   []model.WorkItem        `json:"dependencies"`
	Blockers       []store.WorkItemBlocker `json:"blockers"`
	Runs           []store.RunView         `json:"runs"`
	Commit         *store.RunCommit        `json:"commit,omitempty"`
	Score          *model.IdeaScore        `json:"score,omitempty"`
	EstimateSource string                  `json:"estimate_source"`
	Forecast       store.TokenForecast     `json:"forecast"`
	Model          store.ModelChoice       `json:"model"`
	Actionable     bool                    `json:"actionable"`
}

// goalForProject resolves the goal a work request applies to, preferring the
// active goal and falling back to the latest so a completed goal's items stay
// inspectable.
func (s *Server) goalForProject(r *http.Request, projectID string) (model.Goal, error) {
	goal, err := s.store.CurrentGoal(r.Context(), projectID)
	if errors.Is(err, store.ErrNotFound) {
		goal, err = s.store.LatestGoal(r.Context(), projectID)
	}
	return goal, err
}

func (s *Server) workItems(w http.ResponseWriter, r *http.Request) {
	goal, err := s.goalForProject(r, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"work_items": []model.WorkItem{}})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	query := store.WorkItemQuery{Search: r.URL.Query().Get("q")}
	if statuses := strings.TrimSpace(r.URL.Query().Get("status")); statuses != "" {
		for _, status := range strings.Split(statuses, ",") {
			if trimmed := strings.ToUpper(strings.TrimSpace(status)); trimmed != "" {
				query.Statuses = append(query.Statuses, trimmed)
			}
		}
	}
	if limit, convErr := strconv.Atoi(r.URL.Query().Get("limit")); convErr == nil {
		query.Limit = limit
	}
	items, err := s.store.SearchWorkItems(r.Context(), goal.ID, query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []model.WorkItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"work_items": items, "goal_id": goal.ID})
}

func (s *Server) workItemDetail(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	goal, err := s.goalForProject(r, projectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project has no goal")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail, err := s.store.WorkItemDetails(r.Context(), projectID, goal.ID, r.PathValue("workID"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "work item not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	view := WorkItemDetailView{Item: detail.Item, Dependencies: detail.Dependencies, Blockers: detail.Blockers,
		Runs: detail.Runs, Commit: detail.Commit, Score: detail.Score,
		EstimateSource: detail.EstimateSource, Forecast: detail.Forecast, Model: detail.Model}
	if view.Blockers == nil {
		view.Blockers = []store.WorkItemBlocker{}
	}
	if view.Runs == nil {
		view.Runs = []store.RunView{}
	}
	if view.Dependencies == nil {
		view.Dependencies = []model.WorkItem{}
	}
	view.Actionable = len(view.Blockers) == 0
	writeJSON(w, http.StatusOK, view)
}

// workPlanRequest carries only planning fields. Status transitions keep their
// own endpoint so an edit to a specification can never advance the lifecycle.
type workPlanRequest struct {
	Title           string   `json:"title"`
	Objective       string   `json:"objective"`
	Acceptance      string   `json:"acceptance"`
	ChangeScope     string   `json:"change_scope"`
	Dependencies    []string `json:"dependencies"`
	Risk            string   `json:"risk"`
	Priority        *float64 `json:"priority"`
	Weight          *float64 `json:"weight"`
	EstimatedTokens *int64   `json:"estimated_tokens"`
}

func (s *Server) updateWorkPlan(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	goal, err := s.goalForProject(r, projectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project has no goal")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var request workPlanRequest
	if decodeErr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&request); decodeErr != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+decodeErr.Error())
		return
	}
	if request.Risk != "" && request.Risk != "low" && request.Risk != "medium" && request.Risk != "high" {
		writeError(w, http.StatusBadRequest, "risk must be low, medium, or high")
		return
	}
	item, err := s.store.UpdateWorkItemPlan(r.Context(), goal.ID, r.PathValue("workID"), store.WorkItemPlan{
		Title: request.Title, Objective: request.Objective, Acceptance: request.Acceptance,
		ChangeScope: request.ChangeScope, Dependencies: request.Dependencies, Risk: request.Risk,
		Priority: request.Priority, Weight: request.Weight, EstimatedTokens: request.EstimatedTokens,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "work item not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

// ApprovalDetailView gives a reviewer the grounds for the decision: the exact
// commit, the files it touches, the verification results behind it, where it
// would be applied, and how to undo it if it turns out to be wrong.
type ApprovalDetailView struct {
	Approval      ApprovalView               `json:"approval"`
	Status        string                     `json:"status"`
	Commit        *store.RunCommit           `json:"commit,omitempty"`
	FileChanges   []gitops.FileChange        `json:"file_changes"`
	Verifications []store.VerificationRecord `json:"verifications"`
	Diff          string                     `json:"diff,omitempty"`
	DiffTruncated bool                       `json:"diff_truncated,omitempty"`
	DiffError     string                     `json:"diff_error,omitempty"`
	Stale         bool                       `json:"stale"`
	StaleReason   string                     `json:"stale_reason,omitempty"`
	Rollback      string                     `json:"rollback"`
	ApprovedAt    *time.Time                 `json:"approved_at,omitempty"`
}

func (s *Server) approvalDetail(w http.ResponseWriter, r *http.Request) {
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
	approval, err := s.store.ApprovalByID(r.Context(), projectID, r.PathValue("approvalID"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "approval not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	view := ApprovalDetailView{
		Approval: ApprovalView{ID: approval.ID, ActionType: approval.ActionType, Reason: approval.Reason,
			RequestedAt: approval.RequestedAt, Scope: approval.Scope},
		Status:        approval.Status,
		FileChanges:   []gitops.FileChange{},
		Verifications: []store.VerificationRecord{},
		Rollback:      rollbackHint(approval),
	}
	if !approval.ApprovedAt.IsZero() {
		view.ApprovedAt = &approval.ApprovedAt
	}
	if approval.Scope.CommitSHA == "" {
		writeJSON(w, http.StatusOK, view)
		return
	}
	// A commit recorded later than the approved one means the work item was
	// re-run after review: say so here rather than letting the user discover
	// it when the merge is refused.
	if latest, latestErr := s.store.LatestRunCommitForWork(r.Context(), projectID, approval.Scope.WorkItemID); latestErr == nil {
		view.Commit = &latest
		if latest.CommitSHA != approval.Scope.CommitSHA {
			view.Stale = true
			view.StaleReason = "승인된 커밋 " + shortSHA(approval.Scope.CommitSHA) + " 이후 " + shortSHA(latest.CommitSHA) + " 이(가) 생성되었습니다. 새 변경을 다시 검토해야 합니다."
		}
		if changes, changeErr := s.store.ListRunFileChanges(r.Context(), latest.RunID); changeErr == nil && changes != nil {
			view.FileChanges = changes
		}
		if results, resultErr := s.store.VerificationsForRun(r.Context(), latest.RunID); resultErr == nil && results != nil {
			view.Verifications = results
		}
	} else if !errors.Is(latestErr, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, latestErr.Error())
		return
	}
	if diff, truncated, diffErr := gitops.CommitDiff(r.Context(), project.RepositoryPath, approval.Scope.CommitSHA, diffLimitBytes); diffErr == nil {
		view.Diff, view.DiffTruncated = diff, truncated
	} else {
		view.DiffError = diffErr.Error()
	}
	writeJSON(w, http.StatusOK, view)
}

// rollbackHint states how to undo the action being approved, because approving
// something without knowing the way back is not an informed decision.
func rollbackHint(approval store.Approval) string {
	switch approval.ActionType {
	case store.ApprovalMergeBranch:
		return "되돌리기: goalforge rollback --work-item " + approval.Scope.WorkItemID + " --reason \"...\" (병합 후에는 " + approval.Scope.TargetRef + " 에서 git revert -m 1 " + shortSHA(approval.Scope.CommitSHA) + ")"
	case store.ApprovalPublishBranch:
		return "되돌리기: git push " + approval.Scope.TargetRef + " --delete " + approval.Scope.SourceBranch + " (이미 가져간 사본은 회수할 수 없습니다)"
	default:
		return "되돌리기: 해당 실행의 작업 공간 변경을 goalforge rollback 으로 취소"
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
