package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Outbox kinds.
const (
	// OutboxContinue records that a goal still has work to do. It is written
	// in the same transaction as the outcome that decided so, because the gap
	// between "the state says continue" and "a job exists to continue it" is
	// where a goal silently stops.
	OutboxContinue = "CONTINUE"
)

// OutboxEntry is an intent recorded with the state change that produced it,
// published afterwards. A crash between the two leaves the entry, so the
// follow-up happens on restart instead of being lost.
type OutboxEntry struct {
	ID, ProjectID, Kind, Payload string
	CreatedAt                    time.Time
	PublishedAt                  time.Time
}

// enqueueOutbox writes an intent inside the caller's transaction.
func enqueueOutbox(ctx context.Context, tx *sql.Tx, projectID, kind, payload string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO outbox(id,project_id,kind,payload,created_at) VALUES(?,?,?,?,?)`,
		NewID("OBX"), projectID, kind, payload, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// PendingOutbox lists intents that have not been turned into work yet.
func (s *Store) PendingOutbox(ctx context.Context, projectID string) ([]OutboxEntry, error) {
	query := `SELECT id,project_id,kind,payload,created_at FROM outbox WHERE published_at='' `
	args := []any{}
	if projectID != "" {
		query += `AND project_id=? `
		args = append(args, projectID)
	}
	query += `ORDER BY created_at,id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []OutboxEntry
	for rows.Next() {
		var entry OutboxEntry
		var created string
		if err = rows.Scan(&entry.ID, &entry.ProjectID, &entry.Kind, &entry.Payload, &created); err != nil {
			return nil, err
		}
		entry.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, entry)
	}
	return result, rows.Err()
}

// PublishOutbox turns recorded intents into scheduler jobs. It is safe to call
// repeatedly and from more than one worker: the job's idempotency key comes
// from the entry, so a publish that runs twice produces one job, and an entry
// marked published after the job was created cannot produce a second.
func (s *Store) PublishOutbox(ctx context.Context, projectID string) (int, error) {
	pending, err := s.PendingOutbox(ctx, projectID)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, entry := range pending {
		switch entry.Kind {
		case OutboxContinue:
			if _, err = s.ScheduleRecurringJob(ctx, SchedulerJob{ProjectID: entry.ProjectID, Type: "CONTINUE",
				RunAt: time.Now().UTC(), IdempotencyKey: "continue:" + entry.ProjectID}); err != nil {
				return published, err
			}
		default:
			// An unknown kind is not something to drop silently; leaving it
			// pending keeps it visible rather than losing it.
			continue
		}
		if err = s.markOutboxPublished(ctx, entry.ID); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}

func (s *Store) markOutboxPublished(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE outbox SET published_at=? WHERE id=? AND published_at=''`,
		time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("outbox entry was already published")
	}
	return nil
}
