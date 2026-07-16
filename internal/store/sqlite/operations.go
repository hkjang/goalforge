package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

type EventLog struct {
	ID                         int64
	RunID, Provider, Type, Raw string
	CreatedAt                  time.Time
}

type ProjectMetrics struct {
	RunsTotal, RunsSuccessful, RunsFailed, WorkDone, WorkBlocked  int64
	VerificationTotal, VerificationPassed, SessionCount           int64
	InputTokens, OutputTokens, CachedInputTokens, ReasoningTokens int64
	CostUSD, AverageRunSeconds                                    float64
}

func (s *Store) ProjectMetrics(ctx context.Context, projectID string) (ProjectMetrics, error) {
	var metrics ProjectMetrics
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN state IN ('VERIFYING','CHECKPOINTING','COMPLETED','REPAIR_REQUIRED') THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN state='FAILED' THEN 1 ELSE 0 END),0),COALESCE(AVG(CASE WHEN ended_at IS NOT NULL THEN (julianday(ended_at)-julianday(started_at))*86400 END),0) FROM runs WHERE project_id=?`, projectID).Scan(&metrics.RunsTotal, &metrics.RunsSuccessful, &metrics.RunsFailed, &metrics.AverageRunSeconds)
	if err != nil {
		return metrics, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN w.status='DONE' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN w.status='BLOCKED' THEN 1 ELSE 0 END),0) FROM work_items w JOIN goals g ON g.id=w.goal_id WHERE g.project_id=?`, projectID).Scan(&metrics.WorkDone, &metrics.WorkBlocked); err != nil {
		return metrics, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN v.status='PASSED' THEN 1 ELSE 0 END),0) FROM verification_results v JOIN goals g ON g.id=v.goal_id WHERE g.project_id=?`, projectID).Scan(&metrics.VerificationTotal, &metrics.VerificationPassed); err != nil {
		return metrics, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_session_history WHERE project_id=?`, projectID).Scan(&metrics.SessionCount); err != nil {
		return metrics, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.token_type,COALESCE(SUM(l.amount),0),COALESCE(SUM(l.cost),0) FROM usage_ledger l JOIN runs r ON r.id=l.run_id WHERE r.project_id=? GROUP BY l.token_type`, projectID)
	if err != nil {
		return metrics, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var amount int64
		var cost float64
		if err = rows.Scan(&kind, &amount, &cost); err != nil {
			return metrics, err
		}
		switch kind {
		case "input":
			metrics.InputTokens = amount
		case "output":
			metrics.OutputTokens = amount
		case "cached_input", "cache_creation":
			metrics.CachedInputTokens += amount
		case "reasoning":
			metrics.ReasoningTokens = amount
		case "cost_usd":
			metrics.CostUSD = cost
		}
	}
	return metrics, rows.Err()
}

// CriterionStatus reports whether a completion criterion is satisfied by the
// latest verification evidence.
type CriterionStatus struct {
	Type, ExpectedValue, ActualValue string
	Satisfied                        bool
}

func (s *Store) CriteriaStatus(ctx context.Context, goal model.Goal) ([]CriterionStatus, error) {
	result := make([]CriterionStatus, 0, len(goal.Criteria))
	for _, criterion := range goal.Criteria {
		entry := CriterionStatus{Type: criterion.Type, ExpectedValue: criterion.ExpectedValue}
		var actual, status string
		err := s.db.QueryRowContext(ctx, `SELECT actual_value,status FROM verification_results WHERE goal_id=? AND check_type=? ORDER BY id DESC LIMIT 1`, goal.ID, criterion.Type).Scan(&actual, &status)
		if err == nil {
			entry.ActualValue = actual
			entry.Satisfied = status == "PASSED" && criterionMet(criterion.ExpectedValue, actual)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, nil
}

// RunView is a run summarized for operational displays.
type RunView struct {
	ID, WorkItemID, TaskType, State string
	StartedAt, EndedAt              time.Time
	Tokens                          int64
	CostUSD                         float64
}

func (s *Store) ListRecentRuns(ctx context.Context, projectID string, limit int) ([]RunView, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,COALESCE(r.work_item_id,''),r.task_type,r.state,r.started_at,COALESCE(r.ended_at,''),COALESCE(SUM(CASE WHEN l.token_type<>'cost_usd' THEN l.amount ELSE 0 END),0),COALESCE(SUM(l.cost),0) FROM runs r LEFT JOIN usage_ledger l ON l.run_id=r.id WHERE r.project_id=? GROUP BY r.id ORDER BY r.started_at DESC,r.id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RunView
	for rows.Next() {
		var run RunView
		var started, ended string
		if err = rows.Scan(&run.ID, &run.WorkItemID, &run.TaskType, &run.State, &started, &ended, &run.Tokens, &run.CostUSD); err != nil {
			return nil, err
		}
		run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		run.EndedAt, _ = time.Parse(time.RFC3339Nano, ended)
		result = append(result, run)
	}
	return result, rows.Err()
}

func (s *Store) ListSessions(ctx context.Context, projectID string) ([]SessionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id,provider,session_id,model,status,last_run_id,context_tokens_used,created_at,updated_at,COALESCE(expires_at,''),COALESCE(retention_until,''),replacement_reason FROM provider_session_history WHERE project_id=? ORDER BY provider,created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SessionRecord
	for rows.Next() {
		var session SessionRecord
		var created, updated, expires, retention string
		if err = rows.Scan(&session.ProjectID, &session.Provider, &session.SessionID, &session.Model, &session.Status, &session.LastRunID, &session.ContextTokensUsed, &created, &updated, &expires, &retention, &session.ReplacementReason); err != nil {
			return nil, err
		}
		session.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		session.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		session.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		session.RetentionUntil, _ = time.Parse(time.RFC3339Nano, retention)
		result = append(result, session)
	}
	return result, rows.Err()
}

func (s *Store) ListQuotaWindows(ctx context.Context, providerName string) ([]QuotaWindow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider,account_id,limit_type,status,used_percent,detected_at,quota_reset_at,resume_at,source,confidence,raw_message FROM quota_windows WHERE provider=? ORDER BY account_id,limit_type`, providerName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []QuotaWindow
	for rows.Next() {
		var q QuotaWindow
		var detected string
		var reset, resume sql.NullString
		if err = rows.Scan(&q.Provider, &q.AccountID, &q.LimitType, &q.Status, &q.UsedPercent, &detected, &reset, &resume, &q.Source, &q.Confidence, &q.RawMessage); err != nil {
			return nil, err
		}
		q.DetectedAt, _ = time.Parse(time.RFC3339Nano, detected)
		if reset.Valid {
			parsed, parseErr := time.Parse(time.RFC3339Nano, reset.String)
			if parseErr != nil {
				return nil, parseErr
			}
			q.QuotaResetAt = &parsed
		}
		if resume.Valid {
			parsed, parseErr := time.Parse(time.RFC3339Nano, resume.String)
			if parseErr != nil {
				return nil, parseErr
			}
			q.ResumeAt = &parsed
		}
		result = append(result, q)
	}
	return result, rows.Err()
}

// EventLogsForRun returns a run's redacted provider events in stream order
// for the replay view.
func (s *Store) EventLogsForRun(ctx context.Context, projectID, runID string, limit int) ([]EventLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.run_id,e.provider,e.event_type,CAST(e.raw_payload AS TEXT),e.created_at FROM event_logs e JOIN runs r ON r.id=e.run_id WHERE r.project_id=? AND e.run_id=? ORDER BY e.id LIMIT ?`, projectID, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEventLogs(rows)
}

// PromptForRun returns the audited prompt record (template, hash, redacted
// preview) for a run.
type PromptView struct {
	Template, RenderedHash, RedactedPrompt string
}

func (s *Store) PromptForRun(ctx context.Context, runID string) (PromptView, error) {
	var record PromptView
	err := s.db.QueryRowContext(ctx, `SELECT template,rendered_hash,redacted_prompt FROM prompt_records WHERE run_id=?`, runID).Scan(&record.Template, &record.RenderedHash, &record.RedactedPrompt)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	return record, err
}

func (s *Store) VerificationsForRun(ctx context.Context, runID string) ([]VerificationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT check_type,status,actual_value,command,exit_code,duration_ms,required,output FROM verification_results WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []VerificationRecord
	for rows.Next() {
		var record VerificationRecord
		var durationMS int64
		if err = rows.Scan(&record.CheckType, &record.Status, &record.ActualValue, &record.Command, &record.ExitCode, &durationMS, &record.Required, &record.Output); err != nil {
			return nil, err
		}
		record.RunID = runID
		record.Duration = time.Duration(durationMS) * time.Millisecond
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) ListEventLogs(ctx context.Context, projectID string, limit int) ([]EventLog, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("event log limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.run_id,e.provider,e.event_type,CAST(e.raw_payload AS TEXT),e.created_at FROM event_logs e JOIN runs r ON r.id=e.run_id WHERE r.project_id=? ORDER BY e.id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEventLogs(rows)
}

func scanEventLogs(rows *sql.Rows) ([]EventLog, error) {
	var result []EventLog
	for rows.Next() {
		var event EventLog
		var created string
		if err := rows.Scan(&event.ID, &event.RunID, &event.Provider, &event.Type, &event.Raw, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, event)
	}
	return result, rows.Err()
}

// DailyUsagePoint aggregates one UTC day of run activity for trend charts.
type DailyUsagePoint struct {
	Date    string
	Runs    int64
	Tokens  int64
	CostUSD float64
}

func (s *Store) DailyUsageSeries(ctx context.Context, projectID string, days int) ([]DailyUsagePoint, error) {
	if days <= 0 || days > 90 {
		days = 14
	}
	since := time.Now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	rows, err := s.db.QueryContext(ctx, `SELECT date(r.started_at) AS day,COUNT(DISTINCT r.id),COALESCE(SUM(CASE WHEN l.token_type<>'cost_usd' THEN l.amount ELSE 0 END),0),COALESCE(SUM(l.cost),0) FROM runs r LEFT JOIN usage_ledger l ON l.run_id=r.id WHERE r.project_id=? AND date(r.started_at)>=? GROUP BY day ORDER BY day`, projectID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DailyUsagePoint
	for rows.Next() {
		var point DailyUsagePoint
		if err = rows.Scan(&point.Date, &point.Runs, &point.Tokens, &point.CostUSD); err != nil {
			return nil, err
		}
		result = append(result, point)
	}
	return result, rows.Err()
}

func (s *Store) ListSchedulerJobs(ctx context.Context, projectID string, activeOnly bool) ([]SchedulerJob, error) {
	query := `SELECT id,project_id,job_type,run_at,idempotency_key,status,payload,attempts,owner,COALESCE(lease_until,''),last_error FROM scheduler_jobs WHERE project_id=?`
	if activeOnly {
		query += ` AND status IN ('PENDING','RUNNING')`
	}
	query += ` ORDER BY run_at,id`
	rows, err := s.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SchedulerJob
	for rows.Next() {
		var job SchedulerJob
		var runAt, lease string
		if err = rows.Scan(&job.ID, &job.ProjectID, &job.Type, &runAt, &job.IdempotencyKey, &job.Status, &job.Payload, &job.Attempts, &job.Owner, &lease, &job.LastError); err != nil {
			return nil, err
		}
		job.RunAt, _ = time.Parse(time.RFC3339Nano, runAt)
		job.LeaseUntil, _ = time.Parse(time.RFC3339Nano, lease)
		result = append(result, job)
	}
	return result, rows.Err()
}

func (s *Store) CancelProjectJobs(ctx context.Context, projectID string) (int64, error) {
	var running int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_jobs WHERE project_id=? AND status='RUNNING'`, projectID).Scan(&running); err != nil {
		return 0, err
	}
	if running > 0 {
		return 0, errors.New("cannot cancel while a scheduler job is running")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE scheduler_jobs SET status='CANCELLED',owner='',lease_until=NULL,updated_at=? WHERE project_id=? AND status='PENDING'`, time.Now().UTC().Format(time.RFC3339Nano), projectID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
