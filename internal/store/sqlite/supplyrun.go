package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Why a supply pass ran.
//
// The reason is part of the record because a pass that ran because the branch
// moved and one that ran because the board emptied are answering different
// questions, and a report that cannot tell them apart cannot say whether the
// schedule is doing anything.
const (
	TriggerScheduled     = "SCHEDULED"
	TriggerCommitChanged = "COMMIT_CHANGED"
	TriggerBacklogLow    = "BACKLOG_LOW"
	TriggerManual        = "MANUAL"
)

// Supply pass states.
const (
	SupplyRunning       = "RUNNING"
	SupplyRunCompleted  = "COMPLETED"
	SupplyRunFailed     = "FAILED"
	supplyRunAbandonAge = time.Hour
)

// SupplyCounts is what one pass did.
type SupplyCounts struct {
	Filed, AlreadyFiled, Deferred, Unchecked int
}

// SupplyRun is one pass over a repository.
type SupplyRun struct {
	ID, ProjectID, CommitSHA, Trigger string
	Status, Detail                    string
	StartedAt, EndedAt                time.Time
	SupplyCounts
}

// Key identifies the event this pass answers.
//
// A project, a commit and a reason: two of the three matching is a different
// event. Retrying the same delivery, or two workers seeing the same commit,
// converge here rather than each walking the repository and each filing the
// findings.
func (r SupplyRun) Key() string {
	payload := strings.Join([]string{r.ProjectID, r.CommitSHA, r.Trigger}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))[:32]
}

// BeginSupplyRun claims the right to run a pass for an event.
//
// It reports whether this caller started it. A pass already completed is not
// repeated — repeating it would file its findings a second time. A pass that
// failed is retryable: one bad night must not leave the project unassessed. A
// pass still running is left alone, unless it has been running longer than any
// live pass plausibly would, in which case it was abandoned and the work has
// to be able to continue. An idempotency key that blocks every retry after a
// crash is the failure the key was added to prevent.
func (s *Store) BeginSupplyRun(ctx context.Context, run SupplyRun) (SupplyRun, bool, error) {
	if run.ProjectID == "" || run.CommitSHA == "" || run.Trigger == "" {
		return run, false, errors.New("project, commit, and trigger are required")
	}
	key := run.Key()
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return run, false, err
	}
	defer tx.Rollback()
	existing, found, err := supplyRunByKey(ctx, tx, key)
	if err != nil {
		return run, false, err
	}
	if found {
		switch {
		case existing.Status == SupplyRunCompleted:
			return existing, false, nil
		case existing.Status == SupplyRunning && now.Sub(existing.StartedAt) < supplyRunAbandonAge:
			return existing, false, nil
		}
		// Failed, or running for longer than a pass can plausibly take. Start
		// over against the same key so the event still maps to one record.
		if _, err = tx.ExecContext(ctx, `UPDATE supply_runs SET status=?,detail='',started_at=?,ended_at='',
filed=0,already_filed=0,deferred=0,unchecked=0 WHERE supply_key=?`,
			SupplyRunning, now.Format(time.RFC3339Nano), key); err != nil {
			return run, false, err
		}
		existing.Status, existing.StartedAt, existing.EndedAt = SupplyRunning, now, time.Time{}
		existing.SupplyCounts = SupplyCounts{}
		return existing, true, tx.Commit()
	}
	run.ID = NewID("SUP")
	run.Status, run.StartedAt = SupplyRunning, now
	if _, err = tx.ExecContext(ctx, `INSERT INTO supply_runs(id,supply_key,project_id,commit_sha,trigger_kind,status,detail,started_at,ended_at) VALUES(?,?,?,?,?,?,'',?,'')`,
		run.ID, key, run.ProjectID, run.CommitSHA, run.Trigger, run.Status, now.Format(time.RFC3339Nano)); err != nil {
		return run, false, err
	}
	return run, true, tx.Commit()
}

// FinishSupplyRun records what a pass did.
func (s *Store) FinishSupplyRun(ctx context.Context, key, status, detail string, counts SupplyCounts) error {
	if status != SupplyRunCompleted && status != SupplyRunFailed {
		return fmt.Errorf("%q is not a terminal state", status)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE supply_runs SET status=?,detail=?,ended_at=?,
filed=?,already_filed=?,deferred=?,unchecked=? WHERE supply_key=?`,
		status, detail, time.Now().UTC().Format(time.RFC3339Nano),
		counts.Filed, counts.AlreadyFiled, counts.Deferred, counts.Unchecked, key)
	return err
}

// LastSupplyRun is the most recent pass for a project.
func (s *Store) LastSupplyRun(ctx context.Context, projectID string) (SupplyRun, error) {
	var run SupplyRun
	var started, ended string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,commit_sha,trigger_kind,status,detail,started_at,ended_at,
filed,already_filed,deferred,unchecked FROM supply_runs WHERE project_id=? ORDER BY started_at DESC,id DESC LIMIT 1`, projectID).
		Scan(&run.ID, &run.ProjectID, &run.CommitSHA, &run.Trigger, &run.Status, &run.Detail, &started, &ended,
			&run.Filed, &run.AlreadyFiled, &run.Deferred, &run.Unchecked)
	if errors.Is(err, sql.ErrNoRows) {
		return run, ErrNotFound
	}
	if err != nil {
		return run, err
	}
	run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if ended != "" {
		run.EndedAt, _ = time.Parse(time.RFC3339Nano, ended)
	}
	return run, nil
}

// LastCompletedSupplyRun is the most recent pass that finished, which is what
// a schedule compares against. A pass that failed did not observe anything, so
// treating it as the last look would skip the project until the next window.
func (s *Store) LastCompletedSupplyRun(ctx context.Context, projectID string) (SupplyRun, error) {
	var run SupplyRun
	var started, ended string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,commit_sha,trigger_kind,status,detail,started_at,ended_at,
filed,already_filed,deferred,unchecked FROM supply_runs WHERE project_id=? AND status=? ORDER BY ended_at DESC,id DESC LIMIT 1`,
		projectID, SupplyRunCompleted).
		Scan(&run.ID, &run.ProjectID, &run.CommitSHA, &run.Trigger, &run.Status, &run.Detail, &started, &ended,
			&run.Filed, &run.AlreadyFiled, &run.Deferred, &run.Unchecked)
	if errors.Is(err, sql.ErrNoRows) {
		return run, ErrNotFound
	}
	if err != nil {
		return run, err
	}
	run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	run.EndedAt, _ = time.Parse(time.RFC3339Nano, ended)
	return run, nil
}

func supplyRunByKey(ctx context.Context, tx *sql.Tx, key string) (SupplyRun, bool, error) {
	var run SupplyRun
	var started, ended string
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,commit_sha,trigger_kind,status,detail,started_at,ended_at,
filed,already_filed,deferred,unchecked FROM supply_runs WHERE supply_key=?`, key).
		Scan(&run.ID, &run.ProjectID, &run.CommitSHA, &run.Trigger, &run.Status, &run.Detail, &started, &ended,
			&run.Filed, &run.AlreadyFiled, &run.Deferred, &run.Unchecked)
	if errors.Is(err, sql.ErrNoRows) {
		return run, false, nil
	}
	if err != nil {
		return run, false, err
	}
	run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if ended != "" {
		run.EndedAt, _ = time.Parse(time.RFC3339Nano, ended)
	}
	return run, true, nil
}

// expireSupplyRunForTest ages a running pass so a test can reach the abandoned
// branch without waiting an hour.
func (s *Store) expireSupplyRunForTest(ctx context.Context, run SupplyRun, startedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE supply_runs SET started_at=? WHERE supply_key=?`,
		startedAt.UTC().Format(time.RFC3339Nano), run.Key())
	return err
}

// SupplyRunsSince counts the passes started in a window, which is what a
// discovery budget is spent against.
//
// Every attempt counts, including the ones that failed. A budget that only
// counted successes would let a pass that crashes on every try run without
// limit, which is exactly the loop a budget exists to stop.
func (s *Store) SupplyRunsSince(ctx context.Context, projectID string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM supply_runs WHERE project_id=? AND started_at>=?`,
		projectID, since.UTC().Format(time.RFC3339Nano)).Scan(&count)
	return count, err
}

// BackdateSupplyRunForTest ages a finished pass so a test can reach the
// interval branch without waiting a day.
func (s *Store) BackdateSupplyRunForTest(ctx context.Context, key string, endedAt time.Time) error {
	stamp := endedAt.UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE supply_runs SET ended_at=?,started_at=? WHERE supply_key=?`,
		stamp, stamp, key)
	return err
}
