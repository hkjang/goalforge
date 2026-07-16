package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/audit"
)

type Checkpoint struct {
	ID, ProjectID, RunID, WorkItemID, Provider, Model, SessionID string
	CommitSHA, Branch, DirtyFingerprint, CompletedSummary        string
	VerificationSummary                                          string
	RemainingSteps, NextAction, RiskSummary                      string
	GoalVersion                                                  int
	DirtyFiles                                                   []string
	CreatedAt                                                    time.Time
}

func (s *Store) CreateCheckpoint(ctx context.Context, c Checkpoint) (Checkpoint, error) {
	if c.ProjectID == "" || c.NextAction == "" || c.Provider == "" {
		return c, errors.New("checkpoint project, provider, and next action are required")
	}
	if c.ID == "" {
		c.ID = NewID("CP")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	c.CompletedSummary = audit.RedactString(c.CompletedSummary)
	c.VerificationSummary = audit.RedactString(c.VerificationSummary)
	c.RemainingSteps = audit.RedactString(c.RemainingSteps)
	c.NextAction = audit.RedactString(c.NextAction)
	c.RiskSummary = audit.RedactString(c.RiskSummary)
	dirty, err := json.Marshal(c.DirtyFiles)
	if err != nil {
		return c, err
	}
	var runID any
	if c.RunID != "" {
		runID = c.RunID
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO checkpoints(id,project_id,run_id,goal_version,work_item_id,provider,model,session_id,commit_sha,branch,dirty_files,dirty_fingerprint,completed_summary,verification_summary,remaining_steps,next_action,risk_summary,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.ProjectID, runID, c.GoalVersion, c.WorkItemID, c.Provider, c.Model, c.SessionID, c.CommitSHA, c.Branch, string(dirty), c.DirtyFingerprint, c.CompletedSummary, c.VerificationSummary, c.RemainingSteps, c.NextAction, c.RiskSummary, c.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return c, err
	}
	return c, s.writeContinuity(c)
}

var unsafeFileName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ContinuityPath returns where the human-readable companion of the latest
// checkpoint for projectID is written.
func (s *Store) ContinuityPath(projectID string) string {
	name := strings.Trim(unsafeFileName.ReplaceAllString(projectID, "-"), "-.")
	if name == "" {
		name = "project"
	}
	return filepath.Join(s.stateDir, "continuity", name+".md")
}

// writeContinuity keeps a CONTINUITY.md companion next to the database for
// every checkpoint so a human can pick the project up during a quota wait.
// It lives in the state directory, never in the repository working tree,
// which would dirty the snapshot the checkpoint just recorded.
func (s *Store) writeContinuity(c Checkpoint) error {
	path := s.ContinuityPath(c.ProjectID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(continuityDocument(c)), 0o600)
}

func continuityDocument(c Checkpoint) string {
	section := func(title, body string) string {
		if strings.TrimSpace(body) == "" {
			body = "(기록 없음)"
		}
		return "## " + title + "\n" + body + "\n\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# CONTINUITY — %s\n\n", c.ProjectID)
	fmt.Fprintf(&b, "- 체크포인트: %s (%s)\n", c.ID, c.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- 목표 버전: v%d\n", c.GoalVersion)
	fmt.Fprintf(&b, "- 현재 작업: %s\n", valueOr(c.WorkItemID, "(없음)"))
	fmt.Fprintf(&b, "- 세션: %s %s %s\n", c.Provider, valueOr(c.Model, "(기본 모델)"), valueOr(c.SessionID, "(세션 없음)"))
	fmt.Fprintf(&b, "- 저장소: %s @ %s\n", valueOr(c.Branch, "(브랜치 미상)"), valueOr(c.CommitSHA, "(커밋 미상)"))
	fmt.Fprintf(&b, "- Dirty 파일: %s\n\n", valueOr(strings.Join(c.DirtyFiles, ", "), "(없음)"))
	b.WriteString(section("완료 내역", c.CompletedSummary))
	b.WriteString(section("검증 상태", c.VerificationSummary))
	b.WriteString(section("남은 작업", c.RemainingSteps))
	b.WriteString(section("다음 행동", c.NextAction))
	b.WriteString(section("위험", c.RiskSummary))
	return b.String()
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (s *Store) LatestCheckpoint(ctx context.Context, projectID string) (Checkpoint, error) {
	var c Checkpoint
	var dirty, created string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,COALESCE(run_id,''),goal_version,work_item_id,provider,model,session_id,commit_sha,branch,dirty_files,dirty_fingerprint,completed_summary,verification_summary,remaining_steps,next_action,risk_summary,created_at FROM checkpoints WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, projectID).Scan(&c.ID, &c.ProjectID, &c.RunID, &c.GoalVersion, &c.WorkItemID, &c.Provider, &c.Model, &c.SessionID, &c.CommitSHA, &c.Branch, &dirty, &c.DirtyFingerprint, &c.CompletedSummary, &c.VerificationSummary, &c.RemainingSteps, &c.NextAction, &c.RiskSummary, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal([]byte(dirty), &c.DirtyFiles); err != nil {
		return c, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return c, nil
}

type SchedulerJob struct {
	ID, ProjectID, Type, IdempotencyKey, Status, Payload, Owner, LastError string
	RunAt, LeaseUntil                                                      time.Time
	Attempts                                                               int
}

func (s *Store) ScheduleJob(ctx context.Context, j SchedulerJob) (SchedulerJob, error) {
	if j.ProjectID == "" || j.Type == "" || j.IdempotencyKey == "" || j.RunAt.IsZero() {
		return j, errors.New("job project, type, idempotency key, and run time are required")
	}
	if j.ID == "" {
		j.ID = NewID("JOB")
	}
	if j.Payload == "" {
		j.Payload = "{}"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO scheduler_jobs(id,project_id,job_type,run_at,idempotency_key,status,payload,created_at,updated_at) VALUES(?,?,?,?,?,'PENDING',?,?,?) ON CONFLICT(idempotency_key) DO NOTHING`, j.ID, j.ProjectID, j.Type, j.RunAt.UTC().Format(time.RFC3339Nano), j.IdempotencyKey, j.Payload, now, now)
	if err != nil {
		return j, err
	}
	var lease string
	err = s.db.QueryRowContext(ctx, `SELECT id,project_id,job_type,run_at,idempotency_key,status,payload,attempts,owner,COALESCE(lease_until,''),last_error FROM scheduler_jobs WHERE idempotency_key=?`, j.IdempotencyKey).Scan(&j.ID, &j.ProjectID, &j.Type, &now, &j.IdempotencyKey, &j.Status, &j.Payload, &j.Attempts, &j.Owner, &lease, &j.LastError)
	if err != nil {
		return j, err
	}
	j.RunAt, _ = time.Parse(time.RFC3339Nano, now)
	j.LeaseUntil, _ = time.Parse(time.RFC3339Nano, lease)
	return j, nil
}

func (s *Store) ClaimDueJob(ctx context.Context, now time.Time, owner string, leaseDuration time.Duration) (SchedulerJob, error) {
	var j SchedulerJob
	if owner == "" || leaseDuration <= 0 {
		return j, errors.New("owner and positive lease duration are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return j, err
	}
	defer tx.Rollback()
	stamp := now.UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE scheduler_jobs SET status='PENDING',owner='',lease_until=NULL,updated_at=? WHERE status='RUNNING' AND lease_until<=?`, stamp, stamp); err != nil {
		return j, err
	}
	var runAt string
	err = tx.QueryRowContext(ctx, `SELECT id,project_id,job_type,run_at,idempotency_key,status,payload,attempts,last_error FROM scheduler_jobs WHERE status='PENDING' AND run_at<=? ORDER BY run_at,id LIMIT 1`, stamp).Scan(&j.ID, &j.ProjectID, &j.Type, &runAt, &j.IdempotencyKey, &j.Status, &j.Payload, &j.Attempts, &j.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	j.Owner = owner
	j.LeaseUntil = now.Add(leaseDuration).UTC()
	result, err := tx.ExecContext(ctx, `UPDATE scheduler_jobs SET status='RUNNING',owner=?,lease_until=?,attempts=attempts+1,updated_at=? WHERE id=? AND status='PENDING'`, owner, j.LeaseUntil.Format(time.RFC3339Nano), stamp, j.ID)
	if err != nil {
		return j, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return j, errors.New("job claim lost")
	}
	if err = tx.Commit(); err != nil {
		return j, err
	}
	j.Status = "RUNNING"
	j.Attempts++
	j.RunAt, _ = time.Parse(time.RFC3339Nano, runAt)
	return j, nil
}

func (s *Store) CompleteJob(ctx context.Context, id, owner string) error {
	return s.finishJob(ctx, id, owner, "COMPLETED", time.Time{}, "")
}
func (s *Store) FailJob(ctx context.Context, id, owner, message string) error {
	return s.finishJob(ctx, id, owner, "FAILED", time.Time{}, message)
}
func (s *Store) RescheduleJob(ctx context.Context, id, owner string, runAt time.Time, message string) error {
	return s.finishJob(ctx, id, owner, "PENDING", runAt, message)
}
func (s *Store) finishJob(ctx context.Context, id, owner, status string, runAt time.Time, message string) error {
	query := `UPDATE scheduler_jobs SET status=?,owner='',lease_until=NULL,last_error=?,updated_at=? WHERE id=? AND status='RUNNING' AND owner=?`
	args := []any{status, message, time.Now().UTC().Format(time.RFC3339Nano), id, owner}
	if status == "PENDING" {
		query = `UPDATE scheduler_jobs SET status=?,run_at=?,owner='',lease_until=NULL,last_error=?,updated_at=? WHERE id=? AND status='RUNNING' AND owner=?`
		args = []any{status, runAt.UTC().Format(time.RFC3339Nano), message, time.Now().UTC().Format(time.RFC3339Nano), id, owner}
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("scheduler job is not owned by caller")
	}
	return nil
}

func (s *Store) AcquireLease(ctx context.Context, projectID, owner string, now time.Time, duration time.Duration) error {
	if owner == "" || duration <= 0 {
		return errors.New("owner and positive duration are required")
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT INTO process_leases(project_id,owner,expires_at,heartbeat_at) VALUES(?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at,heartbeat_at=excluded.heartbeat_at WHERE process_leases.expires_at<=? OR process_leases.owner=excluded.owner`, projectID, owner, now.Add(duration).UTC().Format(time.RFC3339Nano), stamp, stamp)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("project %s is leased", projectID)
	}
	return nil
}
func (s *Store) HeartbeatLease(ctx context.Context, projectID, owner string, now time.Time, duration time.Duration) error {
	result, err := s.db.ExecContext(ctx, `UPDATE process_leases SET expires_at=?,heartbeat_at=? WHERE project_id=? AND owner=? AND expires_at>?`, now.Add(duration).UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), projectID, owner, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("lease is not active or not owned")
	}
	return nil
}
func (s *Store) ReleaseLease(ctx context.Context, projectID, owner string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM process_leases WHERE project_id=? AND owner=?`, projectID, owner)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("lease is not owned")
	}
	return nil
}

// EnterQuotaWait atomically preserves continuity and schedules a single resume.
func (s *Store) EnterQuotaWait(ctx context.Context, q QuotaWindow, c Checkpoint, j SchedulerJob) error {
	if q.QuotaResetAt == nil || q.ResumeAt == nil {
		return errors.New("quota reset and resume times are required")
	}
	if c.ProjectID == "" || c.ProjectID != j.ProjectID {
		return errors.New("checkpoint and job project must match")
	}
	if c.ID == "" {
		c.ID = NewID("CP")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if j.ID == "" {
		j.ID = NewID("JOB")
	}
	if j.Payload == "" {
		j.Payload = "{}"
	}
	if j.RunAt.IsZero() {
		j.RunAt = *q.ResumeAt
	}
	if q.DetectedAt.IsZero() {
		q.DetectedAt = time.Now().UTC()
	}
	q.RawMessage = audit.RedactString(q.RawMessage)
	c.CompletedSummary = audit.RedactString(c.CompletedSummary)
	c.VerificationSummary = audit.RedactString(c.VerificationSummary)
	c.RemainingSteps = audit.RedactString(c.RemainingSteps)
	c.NextAction = audit.RedactString(c.NextAction)
	c.RiskSummary = audit.RedactString(c.RiskSummary)
	dirty, err := json.Marshal(c.DirtyFiles)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if c.RunID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET state='DRAINING',ended_at=? WHERE id=? AND state='RUNNING'`, now, c.RunID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE projects SET state='WAITING_QUOTA' WHERE id=? AND state IN ('READY','PREFLIGHT','RUNNING','DRAINING','CHECKPOINTING','RESUMING')`, c.ProjectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("project cannot enter quota wait from current state")
	}
	var runID any
	if c.RunID != "" {
		runID = c.RunID
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO checkpoints(id,project_id,run_id,goal_version,work_item_id,provider,model,session_id,commit_sha,branch,dirty_files,dirty_fingerprint,completed_summary,verification_summary,remaining_steps,next_action,risk_summary,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.ProjectID, runID, c.GoalVersion, c.WorkItemID, c.Provider, c.Model, c.SessionID, c.CommitSHA, c.Branch, string(dirty), c.DirtyFingerprint, c.CompletedSummary, c.VerificationSummary, c.RemainingSteps, c.NextAction, c.RiskSummary, c.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO quota_windows(provider,account_id,limit_type,status,used_percent,detected_at,quota_reset_at,resume_at,source,confidence,raw_message) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,account_id,limit_type) DO UPDATE SET status=excluded.status,used_percent=excluded.used_percent,detected_at=excluded.detected_at,quota_reset_at=excluded.quota_reset_at,resume_at=excluded.resume_at,source=excluded.source,confidence=excluded.confidence,raw_message=excluded.raw_message`, q.Provider, q.AccountID, q.LimitType, q.Status, q.UsedPercent, q.DetectedAt.Format(time.RFC3339Nano), q.QuotaResetAt.Format(time.RFC3339Nano), q.ResumeAt.Format(time.RFC3339Nano), q.Source, q.Confidence, q.RawMessage); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO scheduler_jobs(id,project_id,job_type,run_at,idempotency_key,status,payload,created_at,updated_at) VALUES(?,?,?,?,?,'PENDING',?,?,?) ON CONFLICT(idempotency_key) DO UPDATE SET run_at=excluded.run_at,status=CASE WHEN scheduler_jobs.status='COMPLETED' THEN scheduler_jobs.status ELSE 'PENDING' END,payload=excluded.payload,updated_at=excluded.updated_at`, j.ID, j.ProjectID, j.Type, j.RunAt.UTC().Format(time.RFC3339Nano), j.IdempotencyKey, j.Payload, now, now); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.writeContinuity(c)
}

func (s *Store) BlockBeforeRun(ctx context.Context, projectID, workItemID string, q QuotaWindow, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if q.RawMessage == "" {
		q.RawMessage = reason
	}
	q.RawMessage = audit.RedactString(q.RawMessage)
	if q.DetectedAt.IsZero() {
		q.DetectedAt = time.Now().UTC()
	}
	var reset, resume any
	if q.QuotaResetAt != nil {
		reset = q.QuotaResetAt.Format(time.RFC3339Nano)
	}
	if q.ResumeAt != nil {
		resume = q.ResumeAt.Format(time.RFC3339Nano)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO quota_windows(provider,account_id,limit_type,status,used_percent,detected_at,quota_reset_at,resume_at,source,confidence,raw_message) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,account_id,limit_type) DO UPDATE SET status=excluded.status,used_percent=excluded.used_percent,detected_at=excluded.detected_at,quota_reset_at=excluded.quota_reset_at,resume_at=excluded.resume_at,source=excluded.source,confidence=excluded.confidence,raw_message=excluded.raw_message`, q.Provider, q.AccountID, q.LimitType, q.Status, q.UsedPercent, q.DetectedAt.Format(time.RFC3339Nano), reset, resume, q.Source, q.Confidence, q.RawMessage); err != nil {
		return err
	}
	if workItemID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE id=? AND status='IN_PROGRESS'`, workItemID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE projects SET state='BLOCKED' WHERE id=? AND state IN ('READY','RESUMING','PREFLIGHT')`, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("project cannot be blocked before run from current state")
	}
	return tx.Commit()
}

func (s *Store) DeferWorkForQuotaWarning(ctx context.Context, projectID, workItemID string, q QuotaWindow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if workItemID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE id=? AND status='IN_PROGRESS'`, workItemID); err != nil {
			return err
		}
	}
	var reset, resume any
	if q.QuotaResetAt != nil {
		reset = q.QuotaResetAt.Format(time.RFC3339Nano)
	}
	if q.ResumeAt != nil {
		resume = q.ResumeAt.Format(time.RFC3339Nano)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO quota_windows(provider,account_id,limit_type,status,used_percent,detected_at,quota_reset_at,resume_at,source,confidence,raw_message) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,account_id,limit_type) DO UPDATE SET status=excluded.status,used_percent=excluded.used_percent,detected_at=excluded.detected_at,quota_reset_at=excluded.quota_reset_at,resume_at=excluded.resume_at,source=excluded.source,confidence=excluded.confidence,raw_message=excluded.raw_message`, q.Provider, q.AccountID, q.LimitType, q.Status, q.UsedPercent, q.DetectedAt.Format(time.RFC3339Nano), reset, resume, q.Source, q.Confidence, audit.RedactString(q.RawMessage))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecoverFailedProject releases a FAILED project back to READY and returns
// any in-progress work item to the backlog so a deliberate retry (backoff
// ladder, fallback model) can claim it again.
func (s *Store) RecoverFailedProject(ctx context.Context, projectID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE status='IN_PROGRESS' AND goal_id IN (SELECT id FROM goals WHERE project_id=?)`, projectID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE projects SET state='READY' WHERE id=? AND state='FAILED'`, projectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("project is not in FAILED state")
	}
	return tx.Commit()
}

func (s *Store) FailBeforeRun(ctx context.Context, projectID, workItemID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if workItemID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE id=? AND status='IN_PROGRESS'`, workItemID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE projects SET state='FAILED' WHERE id=? AND state IN ('CREATED','READY','PREFLIGHT','RESUMING')`, projectID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("project cannot fail before run from current state")
	}
	return tx.Commit()
}
