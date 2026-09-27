package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Lease is one worker's tenancy over a project. The generation is what makes a
// returning worker detectable: a lease that expired and was taken over, or a
// project that was cancelled, moves to a new generation, and writes carrying
// the old one are refused rather than applied late.
type Lease struct {
	ProjectID, Owner string
	Generation       int64
	ExpiresAt        time.Time
}

// ErrStaleGeneration means the writer's tenancy has ended. Its work may have
// been correct, but it can no longer confirm anything: something else has been
// running the project since.
var ErrStaleGeneration = errors.New("worker generation is no longer current")

// AcquireGenerationLease takes or renews a project lease. Taking over from an
// expired or absent lease starts a new generation; renewing keeps the current
// one, because the same worker running on is not a change of tenancy.
func (s *Store) AcquireGenerationLease(ctx context.Context, projectID, owner string, now time.Time, duration time.Duration) (Lease, error) {
	var lease Lease
	if owner == "" || duration <= 0 {
		return lease, errors.New("owner and positive duration are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return lease, err
	}
	defer tx.Rollback()
	stamp := now.UTC().Format(time.RFC3339Nano)
	expiry := now.Add(duration).UTC().Format(time.RFC3339Nano)
	var currentOwner, currentExpiry string
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT owner,expires_at,COALESCE(generation,1) FROM process_leases WHERE project_id=?`, projectID).
		Scan(&currentOwner, &currentExpiry, &generation)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		generation = 1
		if _, err = tx.ExecContext(ctx, `INSERT INTO process_leases(project_id,owner,expires_at,heartbeat_at,generation) VALUES(?,?,?,?,?)`,
			projectID, owner, expiry, stamp, generation); err != nil {
			return lease, err
		}
	case err != nil:
		return lease, err
	case currentOwner == owner:
		// Renewal: the same tenancy continues.
		if _, err = tx.ExecContext(ctx, `UPDATE process_leases SET expires_at=?,heartbeat_at=? WHERE project_id=?`, expiry, stamp, projectID); err != nil {
			return lease, err
		}
	case currentExpiry > stamp:
		return lease, fmt.Errorf("project %s is leased by %s until %s", projectID, currentOwner, currentExpiry)
	default:
		// Taking over an expired lease is a new tenancy, so anything the
		// previous holder still has in flight stops being confirmable.
		generation++
		if _, err = tx.ExecContext(ctx, `UPDATE process_leases SET owner=?,expires_at=?,heartbeat_at=?,generation=? WHERE project_id=?`,
			owner, expiry, stamp, generation, projectID); err != nil {
			return lease, err
		}
	}
	lease = Lease{ProjectID: projectID, Owner: owner, Generation: generation}
	lease.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiry)
	return lease, tx.Commit()
}

// BumpGeneration ends the current tenancy without granting a new one. Cancel
// uses it: the point is that whatever is in flight can no longer confirm, not
// that someone else takes over.
func (s *Store) BumpGeneration(ctx context.Context, projectID string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(generation,1) FROM process_leases WHERE project_id=?`, projectID).Scan(&generation)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if errors.Is(err, sql.ErrNoRows) {
		// No lease has ever been taken, but the next one must not be able to
		// claim the generation an in-flight writer already recorded.
		if _, err = tx.ExecContext(ctx, `INSERT INTO process_leases(project_id,owner,expires_at,heartbeat_at,generation) VALUES(?,'',?,?,2)`,
			projectID, now, now); err != nil {
			return 0, err
		}
		return 2, tx.Commit()
	}
	if err != nil {
		return 0, err
	}
	generation++
	if _, err = tx.ExecContext(ctx, `UPDATE process_leases SET generation=?,owner='',expires_at=?,heartbeat_at=? WHERE project_id=?`,
		generation, now, now, projectID); err != nil {
		return 0, err
	}
	return generation, tx.Commit()
}

// CurrentGeneration reports the generation a write must carry to be accepted.
func (s *Store) CurrentGeneration(ctx context.Context, projectID string) (int64, error) {
	var generation int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(generation,1) FROM process_leases WHERE project_id=?`, projectID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	return generation, err
}

// Fence refuses a write from a tenancy that has ended. A worker whose lease
// expired may well have done correct work, but it cannot confirm state:
// something else has been running the project since, and a late confirmation
// would overwrite that.
func (s *Store) Fence(ctx context.Context, lease Lease) error {
	if lease.ProjectID == "" || lease.Generation == 0 {
		// An unfenced caller is the operator running a command directly, which
		// is the ordinary case and is not something to refuse.
		return nil
	}
	current, err := s.CurrentGeneration(ctx, lease.ProjectID)
	if err != nil {
		return err
	}
	if current != lease.Generation {
		return fmt.Errorf("%w: holding generation %d, project is at %d", ErrStaleGeneration, lease.Generation, current)
	}
	return nil
}
