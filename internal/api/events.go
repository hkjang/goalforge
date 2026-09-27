package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// eventPollInterval is how often a live stream looks for new events. The
// stream holds the connection open, so this is a database read, not a full
// run-detail fetch as the old three-second poll was.
const eventPollInterval = time.Second

// runEvents returns only the events after the given ID. A live view asks for
// what it has not seen rather than re-reading the run.
func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	projectID, runID := r.PathValue("id"), r.PathValue("runID")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	run, err := s.store.RunByID(r.Context(), projectID, runID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	events, err := s.store.EventLogsSince(r.Context(), projectID, runID, after, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, eventPage(run, events))
}

func eventPage(run store.RunView, events []store.EventLog) map[string]any {
	last := int64(0)
	if len(events) > 0 {
		last = events[len(events)-1].ID
	}
	if events == nil {
		events = []store.EventLog{}
	}
	return map[string]any{"events": events, "last_id": last, "state": run.State,
		"tokens": run.Tokens, "cost_usd": run.CostUSD, "server_time": time.Now().UTC()}
}

// runStream pushes run events over Server-Sent Events. The client gets a
// "state" event whenever the run's state changes and a heartbeat while nothing
// happens, so a silent connection is distinguishable from a stalled run.
func (s *Server) runStream(w http.ResponseWriter, r *http.Request) {
	projectID, runID := r.PathValue("id"), r.PathValue("runID")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported by this server")
		return
	}
	if _, err := s.store.RunByID(r.Context(), projectID, runID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if after == 0 {
		after, _ = strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	}
	send := func(name string, payload any, id int64) bool {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if id > 0 {
			fmt.Fprintf(w, "id: %d\n", id)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
		flusher.Flush()
		return true
	}
	ticker := time.NewTicker(eventPollInterval)
	defer ticker.Stop()
	lastState := ""
	for {
		run, err := s.store.RunByID(r.Context(), projectID, runID)
		if err != nil {
			send("error", map[string]string{"error": err.Error()}, 0)
			return
		}
		events, err := s.store.EventLogsSince(r.Context(), projectID, runID, after, 200)
		if err != nil {
			send("error", map[string]string{"error": err.Error()}, 0)
			return
		}
		if len(events) > 0 {
			after = events[len(events)-1].ID
			if !send("events", eventPage(run, events), after) {
				return
			}
		}
		if run.State != lastState {
			lastState = run.State
			send("state", eventPage(run, nil), 0)
		}
		// A finished run has nothing more to stream; saying so lets the client
		// close instead of reconnecting forever.
		if isTerminalRunState(run.State) {
			send("done", map[string]string{"state": run.State}, 0)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func isTerminalRunState(state string) bool {
	switch state {
	case "COMPLETED", "FAILED", "CANCELLED", "REPAIR_REQUIRED", "CHECKPOINTING":
		return true
	}
	return false
}
