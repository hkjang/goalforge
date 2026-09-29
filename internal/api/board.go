package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// board serves the control screen: every work item by column, with the reason
// each one cannot proceed taken from the same place the runner takes it.
func (s *Server) board(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	goal, err := s.goalForProject(r, projectID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, store.Board{Columns: []store.BoardColumn{}})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	query := store.BoardQuery{Search: r.URL.Query().Get("q")}
	for _, status := range strings.Split(r.URL.Query().Get("status"), ",") {
		if trimmed := strings.ToUpper(strings.TrimSpace(status)); trimmed != "" {
			query.Statuses = append(query.Statuses, trimmed)
		}
	}
	query.FailedVerificationOnly = r.URL.Query().Get("verification") == "failed"
	if limit, convErr := strconv.Atoi(r.URL.Query().Get("per_column")); convErr == nil {
		query.PerColumn = limit
	}
	board, err := s.store.BoardFor(r.Context(), projectID, goal, query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, board)
}

// transitionRequest carries the version the board was looking at, so an edit
// made against a stale view is refused rather than overwriting whatever
// happened in between.
type transitionRequest struct {
	Status  string `json:"status"`
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

// transitionWork moves a card. The rule about which moves a person may make
// lives in the store and is shared with the CLI and MCP, so the board cannot
// be the one surface with a different answer.
func (s *Server) transitionWork(w http.ResponseWriter, r *http.Request) {
	projectID, workID := r.PathValue("id"), r.PathValue("workID")
	var request transitionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	goal, err := s.goalForProject(r, projectID)
	if err != nil {
		writeError(w, http.StatusConflict, "no goal for this project")
		return
	}
	item, err := s.store.ApplyManualTransition(r.Context(), goal.ID, workID,
		strings.ToUpper(strings.TrimSpace(request.Status)), request.Version)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "work item not found")
		return
	case errors.Is(err, store.ErrStaleWorkItem):
		// 409 rather than 400: nothing about the request was malformed, the
		// world moved. The board's remedy is to reload, not to retry.
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		var refusal *store.TransitionRefusal
		if errors.As(err, &refusal) {
			writeError(w, http.StatusForbidden, refusal.Reason)
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}
