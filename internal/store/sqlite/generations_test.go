package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func leaseFixture(t *testing.T) (context.Context, *Store, model.Project) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	return ctx, s, project
}

// AT-06: the lease expires, another worker takes over, and the original comes
// back to report completion. Its work may have been fine, but something else
// has been running the project since, so it cannot confirm state.
func TestExpiredWorkerCannotConfirmAfterTakeover(t *testing.T) {
	ctx, s, project := leaseFixture(t)
	now := time.Now().UTC()
	first, err := s.AcquireGenerationLease(ctx, project.ID, "worker-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Fence(ctx, first); err != nil {
		t.Fatalf("the holder must be able to write: %v", err)
	}
	// Renewing is the same tenancy, not a new one.
	renewed, err := s.AcquireGenerationLease(ctx, project.ID, "worker-a", now.Add(30*time.Second), time.Minute)
	if err != nil || renewed.Generation != first.Generation {
		t.Fatalf("renewal must keep the generation: %+v err=%v", renewed, err)
	}
	// A second worker cannot barge in while the lease is live.
	if _, err = s.AcquireGenerationLease(ctx, project.ID, "worker-b", now.Add(40*time.Second), time.Minute); err == nil {
		t.Fatal("a live lease must not be stealable")
	}
	// Once it expires, the takeover starts a new tenancy.
	second, err := s.AcquireGenerationLease(ctx, project.ID, "worker-b", now.Add(10*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation <= first.Generation {
		t.Fatalf("takeover must start a new generation: %d then %d", first.Generation, second.Generation)
	}
	if err = s.Fence(ctx, second); err != nil {
		t.Fatalf("the new holder must be able to write: %v", err)
	}
	if err = s.Fence(ctx, first); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("the replaced worker must be refused: %v", err)
	}
}

// AT-07: cancelling ends the tenancy. A run already under way may finish its
// work, but nothing after the cancel is confirmed or started.
func TestCancelEndsTheCurrentTenancy(t *testing.T) {
	ctx, s, project := leaseFixture(t)
	now := time.Now().UTC()
	held, err := s.AcquireGenerationLease(ctx, project.ID, "worker-a", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,project_id,provider,state,started_at) VALUES('R1',?,'codex','RUNNING',?)`,
		project.ID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestRunControl(ctx, project.ID, "CANCEL"); err != nil {
		t.Fatal(err)
	}
	if err = s.Fence(ctx, held); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("a cancelled tenancy must not confirm anything: %v", err)
	}
	// A worker starting after the cancel gets a live tenancy of its own.
	next, err := s.AcquireGenerationLease(ctx, project.ID, "worker-b", now.Add(time.Second), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Fence(ctx, next); err != nil {
		t.Fatalf("a tenancy taken after the cancel is current: %v", err)
	}
	if next.Generation <= held.Generation {
		t.Fatalf("generations must move forward: %d then %d", held.Generation, next.Generation)
	}
}

// A caller with no lease is the operator running a command directly, which is
// the ordinary case: fencing must not turn every direct command into a
// permission error.
func TestFenceIgnoresCallersWithoutALease(t *testing.T) {
	ctx, s, _ := leaseFixture(t)
	if err := s.Fence(ctx, Lease{}); err != nil {
		t.Fatalf("an unfenced caller must not be refused: %v", err)
	}
}

// Pausing is not cancelling: the work is meant to continue afterwards, so the
// tenancy survives.
func TestPauseKeepsTheTenancy(t *testing.T) {
	ctx, s, project := leaseFixture(t)
	now := time.Now().UTC()
	held, err := s.AcquireGenerationLease(ctx, project.ID, "worker-a", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,project_id,provider,state,started_at) VALUES('R1',?,'codex','RUNNING',?)`,
		project.ID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestRunControl(ctx, project.ID, "PAUSE"); err != nil {
		t.Fatal(err)
	}
	if err = s.Fence(ctx, held); err != nil {
		t.Fatalf("a paused worker must still be able to check point its work: %v", err)
	}
}
