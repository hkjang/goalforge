package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
)

func TestSessionHistoryExpiryInvalidationAndPruning(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	project := model.Project{ID: "P-SESS", Name: "sessions", RepositoryPath: "/sessions", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	record := func(runID, sessionID string) {
		t.Helper()
		if err = s.StartRun(ctx, RunRecord{ID: runID, ProjectID: project.ID, Provider: "codex", Model: "gpt"}); err != nil {
			t.Fatal(err)
		}
		event := provider.Event{Type: provider.EventSessionStarted, RunID: runID, SessionID: sessionID, Raw: json.RawMessage(`{"type":"session"}`)}
		if err = s.RecordProviderEvent(ctx, project.ID, event); err != nil {
			t.Fatal(err)
		}
		if err = s.FinishRun(ctx, runID, "FAILED", "READY"); err != nil {
			t.Fatal(err)
		}
	}
	record("R1", "S1")
	record("R2", "S2")
	sessions, err := s.ListSessions(ctx, project.ID)
	if err != nil || len(sessions) != 2 || sessions[0].SessionID != "S2" || sessions[0].Status != "ACTIVE" || sessions[1].Status != "REPLACED" {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
	if err = s.SetSessionExpiry(ctx, "codex", "S2", time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActiveSession(ctx, project.ID, "codex"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session remained active: %v", err)
	}
	if err = s.InvalidateSession(ctx, project.ID, "codex", "S2", "expired", 0); err != nil {
		t.Fatal(err)
	}
	pruned, err := s.PruneSessions(ctx, time.Now().UTC().Add(time.Second))
	if err != nil || pruned != 1 {
		t.Fatalf("pruned=%d err=%v", pruned, err)
	}
}

func TestLegacyActiveSessionMigratesIntoHistory(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	project := model.Project{ID: "P-LEGACY", Name: "legacy", RepositoryPath: "/legacy", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO provider_sessions(project_id,provider,session_id,status,last_run_id,updated_at) VALUES(?,?,?,?,?,?)`, project.ID, "codex", "legacy-session", "ACTIVE", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `DROP TABLE provider_session_history`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	session, err := s.ActiveSession(ctx, project.ID, "codex")
	if err != nil || session.SessionID != "legacy-session" {
		t.Fatalf("session=%+v err=%v", session, err)
	}
}
