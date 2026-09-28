package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RetentionReport says what a prune would remove, or did.
type RetentionReport struct {
	Before          time.Time
	Applied         bool
	EventBodies     int64
	EventBytes      int64
	PromptBodies    int64
	PromptBytes     int64
	SessionsDropped int64
	// SizeBefore and SizeAfter are the database file's own accounting. The
	// space is not returned to the filesystem until a vacuum, which is said
	// rather than left for the operator to discover from `ls`.
	SizeBefore, SizeAfter int64
}

// Reclaimable is the total body bytes the prune covers.
func (r RetentionReport) Reclaimable() int64 { return r.EventBytes + r.PromptBytes }

// Empty reports whether there is nothing to do.
func (r RetentionReport) Empty() bool {
	return r.EventBodies == 0 && r.PromptBodies == 0 && r.SessionsDropped == 0
}

// Prune drops the bulk of finished runs while keeping the record that they
// happened.
//
// Provider event payloads and prompt bodies are what actually grow: every
// event a provider emits is stored whole, and a worker left running for months
// accumulates them without limit. Nothing ever removed them.
//
// The bodies go and the rows stay. An event keeps its type, its time, and its
// hash; a prompt keeps its template and its hash. So the audit still answers
// "what happened and when", and stops answering "in exactly these words" —
// which is the part that costs gigabytes and the part least often needed.
//
// Evidence and approvals are never touched. They are what the goal's
// completion rests on, and the integrity chain covers them: removing one would
// make `integrity verify` report tampering, which is correct — it would be
// indistinguishable from someone editing the record.
func (s *Store) Prune(ctx context.Context, before time.Time, apply bool) (RetentionReport, error) {
	report := RetentionReport{Before: before.UTC(), Applied: apply}
	if before.IsZero() {
		return report, errors.New("prune needs a cutoff")
	}
	if before.After(time.Now().UTC()) {
		// A cutoff in the future would take everything, including runs still
		// being verified.
		return report, errors.New("기준 시각이 미래입니다. 그러면 검증 중인 실행까지 지워집니다")
	}
	cutoff := before.UTC().Format(time.RFC3339Nano)
	var err error
	if report.SizeBefore, err = s.databaseSize(ctx); err != nil {
		return report, err
	}
	// Only runs that have ended: a run still in flight is still using these.
	const eventScope = `FROM event_logs e JOIN runs r ON r.id=e.run_id
WHERE r.ended_at IS NOT NULL AND r.ended_at<=? AND length(e.raw_payload)>0`
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(e.raw_payload)),0) `+eventScope, cutoff).
		Scan(&report.EventBodies, &report.EventBytes); err != nil {
		return report, err
	}
	const promptScope = `FROM prompt_records p JOIN runs r ON r.id=p.run_id
WHERE r.ended_at IS NOT NULL AND r.ended_at<=? AND (length(p.redacted_prompt)>0 OR length(COALESCE(p.encrypted_prompt,''))>0)`
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(p.redacted_prompt)+length(COALESCE(p.encrypted_prompt,''))),0) `+promptScope, cutoff).
		Scan(&report.PromptBodies, &report.PromptBytes); err != nil {
		return report, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_session_history WHERE status<>'ACTIVE' AND retention_until IS NOT NULL AND retention_until<=?`, cutoff).
		Scan(&report.SessionsDropped); err != nil {
		return report, err
	}
	if !apply {
		report.SizeAfter = report.SizeBefore
		return report, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE event_logs SET raw_payload=x'' WHERE id IN (SELECT e.id `+eventScope+`)`, cutoff); err != nil {
		return report, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE prompt_records SET redacted_prompt='',encrypted_prompt=NULL WHERE id IN (SELECT p.id `+promptScope+`)`, cutoff); err != nil {
		return report, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM provider_session_history WHERE status<>'ACTIVE' AND retention_until IS NOT NULL AND retention_until<=?`, cutoff); err != nil {
		return report, err
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	if report.SizeAfter, err = s.databaseSize(ctx); err != nil {
		return report, err
	}
	return report, nil
}

// Vacuum returns freed pages to the filesystem. It is separate because it
// rewrites the whole database and wants a moment when nothing else is running.
func (s *Store) Vacuum(ctx context.Context) (int64, int64, error) {
	before, err := s.databaseSize(ctx)
	if err != nil {
		return 0, 0, err
	}
	if _, err = s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return before, before, err
	}
	after, err := s.databaseSize(ctx)
	return before, after, err
}

func (s *Store) databaseSize(ctx context.Context) (int64, error) {
	var pageCount, pageSize int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, err
	}
	return pageCount * pageSize, nil
}

// StorageBreakdown is what the database is actually spending its space on, so
// an operator can tell whether pruning would help before running it.
type StorageBreakdown struct {
	Total int64
	Rows  []StorageRow
}

// StorageRow is one table's row count and approximate body size.
type StorageRow struct {
	Table string
	Rows  int64
	Bytes int64
}

// Storage reports where the space went.
func (s *Store) Storage(ctx context.Context) (StorageBreakdown, error) {
	var breakdown StorageBreakdown
	total, err := s.databaseSize(ctx)
	if err != nil {
		return breakdown, err
	}
	breakdown.Total = total
	for _, probe := range []struct{ table, bytes string }{
		{"event_logs", "length(raw_payload)"},
		{"prompt_records", "length(redacted_prompt)+length(COALESCE(encrypted_prompt,''))"},
		{"verification_results", "length(output)"},
		{"runs", "0"},
		{"usage_ledger", "0"},
		{"audit_chain", "0"},
	} {
		var row StorageRow
		row.Table = probe.table
		query := fmt.Sprintf(`SELECT COUNT(*),COALESCE(SUM(%s),0) FROM %s`, probe.bytes, probe.table)
		if err = s.db.QueryRowContext(ctx, query).Scan(&row.Rows, &row.Bytes); err != nil {
			return breakdown, err
		}
		breakdown.Rows = append(breakdown.Rows, row)
	}
	return breakdown, nil
}
