package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/audit"
	"github.com/goalforge/goalforge/internal/model"
)

type HandoffCheckpoint struct {
	GoalVersion                             int
	WorkItemID, CommitSHA, Branch           string
	DirtyFiles                              []string
	CompletedSummary, VerificationSummary   string
	RemainingSteps, NextAction, RiskSummary string
}

type HandoffContent struct {
	Goal       model.Goal        `json:"goal"`
	WorkItems  []model.WorkItem  `json:"work_items"`
	Checkpoint HandoffCheckpoint `json:"checkpoint"`
}

type ProviderHandoff struct {
	ID, ProjectID, FromProvider, ToProvider, ToModel, Reason, Status, ContentJSON, ConsumedRunID string
	GoalVersion                                                                                  int
	CreatedAt                                                                                    time.Time
}

func (s *Store) SwitchProvider(ctx context.Context, projectID, toProvider, toModel, reason string) (ProviderHandoff, error) {
	var result ProviderHandoff
	if projectID == "" || toProvider == "" || reason == "" {
		return result, errors.New("project, target provider, and reason are required")
	}
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		return result, err
	}
	if project.Provider == toProvider && project.Model == toModel {
		return result, errors.New("target provider and model are already active")
	}
	if project.State == "RUNNING" || project.State == "PREFLIGHT" || project.State == "DRAINING" || project.State == "VERIFYING" || project.State == "CHECKPOINTING" || project.State == "RESUMING" || project.State == "WAITING_QUOTA" {
		return result, errors.New("provider cannot be switched while project execution is active or scheduled")
	}
	goal, err := s.CurrentGoal(ctx, projectID)
	if err != nil {
		return result, err
	}
	items, err := s.ListWorkItems(ctx, goal.ID)
	if err != nil {
		return result, err
	}
	content := HandoffContent{Goal: goal, WorkItems: items}
	if checkpoint, checkpointErr := s.LatestCheckpoint(ctx, projectID); checkpointErr == nil {
		content.Checkpoint = HandoffCheckpoint{GoalVersion: checkpoint.GoalVersion, WorkItemID: checkpoint.WorkItemID, CommitSHA: checkpoint.CommitSHA, Branch: checkpoint.Branch, DirtyFiles: checkpoint.DirtyFiles, CompletedSummary: checkpoint.CompletedSummary, VerificationSummary: checkpoint.VerificationSummary, RemainingSteps: checkpoint.RemainingSteps, NextAction: checkpoint.NextAction, RiskSummary: checkpoint.RiskSummary}
	} else if !errors.Is(checkpointErr, ErrNotFound) {
		return result, checkpointErr
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return result, err
	}
	now := time.Now().UTC()
	result = ProviderHandoff{ID: NewID("HANDOFF"), ProjectID: projectID, FromProvider: project.Provider, ToProvider: toProvider, ToModel: toModel, Reason: audit.RedactString(reason), Status: "PENDING", ContentJSON: audit.RedactString(string(raw)), GoalVersion: goal.Version, CreatedAt: now}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	retention := now.Add(30 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE provider_session_history SET status='HANDOFF',updated_at=?,retention_until=?,replacement_reason=? WHERE project_id=? AND provider=? AND status='ACTIVE'`, now.Format(time.RFC3339Nano), retention, "provider switched to "+toProvider, projectID, project.Provider); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE provider_sessions SET status='HANDOFF',updated_at=? WHERE project_id=? AND provider=? AND status='ACTIVE'`, now.Format(time.RFC3339Nano), projectID, project.Provider); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO provider_handoffs(id,project_id,from_provider,to_provider,to_model,goal_version,reason,content_json,status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, result.ID, projectID, project.Provider, toProvider, toModel, goal.Version, result.Reason, result.ContentJSON, result.Status, now.Format(time.RFC3339Nano)); err != nil {
		return result, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE projects SET provider=?,model=? WHERE id=? AND provider=? AND state NOT IN ('RUNNING','PREFLIGHT','DRAINING','VERIFYING','CHECKPOINTING','RESUMING','WAITING_QUOTA')`, toProvider, toModel, projectID, project.Provider)
	if err != nil {
		return result, err
	}
	if rows, _ := updated.RowsAffected(); rows != 1 {
		return result, errors.New("project provider changed concurrently")
	}
	return result, tx.Commit()
}

func (s *Store) PendingHandoff(ctx context.Context, projectID, providerName string) (ProviderHandoff, error) {
	var h ProviderHandoff
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,from_provider,to_provider,to_model,goal_version,reason,content_json,status,created_at,COALESCE(consumed_run_id,'') FROM provider_handoffs WHERE project_id=? AND to_provider=? AND status='PENDING' ORDER BY created_at DESC LIMIT 1`, projectID, providerName).Scan(&h.ID, &h.ProjectID, &h.FromProvider, &h.ToProvider, &h.ToModel, &h.GoalVersion, &h.Reason, &h.ContentJSON, &h.Status, &created, &h.ConsumedRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	h.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return h, err
}

func (s *Store) ConsumeHandoff(ctx context.Context, handoffID, runID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE provider_handoffs SET status='CONSUMED',consumed_run_id=? WHERE id=? AND status='PENDING'`, runID, handoffID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrNotFound
	}
	return nil
}
