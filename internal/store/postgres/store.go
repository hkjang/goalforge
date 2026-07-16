package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	store "github.com/goalforge/goalforge/internal/store/sqlite"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Store struct{ db *sql.DB }

func Open(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("PostgreSQL DSN is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	s := &Store{db: db}
	if err = s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS goalforge_schema_versions (
 version BIGINT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS scheduler_jobs (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, job_type TEXT NOT NULL, run_at TIMESTAMPTZ NOT NULL,
 idempotency_key TEXT NOT NULL UNIQUE, status TEXT NOT NULL DEFAULT 'PENDING', payload JSONB NOT NULL DEFAULT '{}'::jsonb,
 attempts INTEGER NOT NULL DEFAULT 0, owner TEXT NOT NULL DEFAULT '', lease_until TIMESTAMPTZ,
 last_error TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_scheduler_due ON scheduler_jobs(status,run_at,lease_until);
CREATE TABLE IF NOT EXISTS process_leases (
 project_id TEXT PRIMARY KEY, owner TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL,
 heartbeat_at TIMESTAMPTZ NOT NULL
);
INSERT INTO goalforge_schema_versions(version) VALUES(1) ON CONFLICT DO NOTHING;`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

func (s *Store) ScheduleJob(ctx context.Context, job store.SchedulerJob) (store.SchedulerJob, error) {
	if job.ProjectID == "" || job.Type == "" || job.IdempotencyKey == "" || job.RunAt.IsZero() {
		return job, errors.New("job project, type, idempotency key, and run time are required")
	}
	if job.ID == "" {
		job.ID = store.NewID("JOB")
	}
	if job.Payload == "" {
		job.Payload = "{}"
	}
	err := s.db.QueryRowContext(ctx, `INSERT INTO scheduler_jobs(id,project_id,job_type,run_at,idempotency_key,status,payload) VALUES($1,$2,$3,$4,$5,'PENDING',$6::jsonb) ON CONFLICT(idempotency_key) DO UPDATE SET idempotency_key=excluded.idempotency_key RETURNING id,project_id,job_type,run_at,idempotency_key,status,payload::text,attempts,owner,COALESCE(lease_until,'epoch'),last_error`, job.ID, job.ProjectID, job.Type, job.RunAt.UTC(), job.IdempotencyKey, job.Payload).Scan(&job.ID, &job.ProjectID, &job.Type, &job.RunAt, &job.IdempotencyKey, &job.Status, &job.Payload, &job.Attempts, &job.Owner, &job.LeaseUntil, &job.LastError)
	return job, err
}

func (s *Store) ClaimDueJob(ctx context.Context, now time.Time, owner string, lease time.Duration) (store.SchedulerJob, error) {
	var job store.SchedulerJob
	if owner == "" || lease <= 0 {
		return job, errors.New("owner and positive lease are required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return job, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `WITH candidate AS (
 SELECT id FROM scheduler_jobs
 WHERE run_at<=$1 AND (status='PENDING' OR (status='RUNNING' AND lease_until<=$1))
 ORDER BY run_at,id FOR UPDATE SKIP LOCKED LIMIT 1
) UPDATE scheduler_jobs j SET status='RUNNING',owner=$2,lease_until=$3,attempts=j.attempts+1,updated_at=$1
FROM candidate WHERE j.id=candidate.id
RETURNING j.id,j.project_id,j.job_type,j.run_at,j.idempotency_key,j.status,j.payload::text,j.attempts,j.owner,j.lease_until,j.last_error`, now.UTC(), owner, now.UTC().Add(lease)).Scan(&job.ID, &job.ProjectID, &job.Type, &job.RunAt, &job.IdempotencyKey, &job.Status, &job.Payload, &job.Attempts, &job.Owner, &job.LeaseUntil, &job.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return job, store.ErrNotFound
	}
	if err != nil {
		return job, err
	}
	return job, tx.Commit()
}

func (s *Store) CompleteJob(ctx context.Context, id, owner string) error {
	return s.finishJob(ctx, id, owner, "COMPLETED", "")
}

func (s *Store) FailJob(ctx context.Context, id, owner, message string) error {
	return s.finishJob(ctx, id, owner, "FAILED", message)
}

func (s *Store) finishJob(ctx context.Context, id, owner, status, message string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE scheduler_jobs SET status=$1,last_error=$2,owner='',lease_until=NULL,updated_at=now() WHERE id=$3 AND status='RUNNING' AND owner=$4`, status, message, id, owner)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("scheduler job lease ownership lost")
	}
	return nil
}

func (s *Store) RescheduleJob(ctx context.Context, id, owner string, runAt time.Time, message string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE scheduler_jobs SET status='PENDING',run_at=$1,last_error=$2,owner='',lease_until=NULL,updated_at=now() WHERE id=$3 AND status='RUNNING' AND owner=$4`, runAt.UTC(), message, id, owner)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("scheduler job lease ownership lost")
	}
	return nil
}

func (s *Store) AcquireLease(ctx context.Context, projectID, owner string, now time.Time, duration time.Duration) error {
	if projectID == "" || owner == "" || duration <= 0 {
		return errors.New("project, owner, and positive duration are required")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO process_leases(project_id,owner,expires_at,heartbeat_at) VALUES($1,$2,$3,$4) ON CONFLICT(project_id) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at,heartbeat_at=excluded.heartbeat_at WHERE process_leases.expires_at<=excluded.heartbeat_at OR process_leases.owner=excluded.owner`, projectID, owner, now.UTC().Add(duration), now.UTC())
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("project lease is already held")
	}
	return nil
}

func (s *Store) HeartbeatLease(ctx context.Context, projectID, owner string, now time.Time, duration time.Duration) error {
	result, err := s.db.ExecContext(ctx, `UPDATE process_leases SET expires_at=$1,heartbeat_at=$2 WHERE project_id=$3 AND owner=$4 AND expires_at>$2`, now.UTC().Add(duration), now.UTC(), projectID, owner)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("project lease ownership lost")
	}
	return nil
}

func (s *Store) ReleaseLease(ctx context.Context, projectID, owner string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM process_leases WHERE project_id=$1 AND owner=$2`, projectID, owner)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("project lease ownership lost")
	}
	return nil
}
