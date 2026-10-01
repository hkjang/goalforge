package sqlite

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"
)

// Short enough that a lease period fits inside a test. The ratio is what
// matters: the work outlasts one period, which is the ordinary case for a
// provider run against a two-hour lease.
const (
	heldLeaseDuration  = 150 * time.Millisecond
	heldLeaseHeartbeat = 30 * time.Millisecond
)

// The lease exists so two workers do not run one project at once. It was taken
// for a fixed two hours and never renewed, so a run that took longer lost it —
// not because the worker died, but because nobody renewed it. Then another
// worker could take the project over while the first was still writing to the
// same working tree, and the fence refused the first worker's result at the
// end for a tenancy that only ended through neglect.
//
// HeartbeatLease was written for exactly this and had no caller.
func TestALeaseIsKeptWhileTheWorkRuns(t *testing.T) {
	ctx, db, project := leaseFixture(t)
	release, err := db.HoldLease(ctx, project.ID, "RUN-1", heldLeaseDuration, heldLeaseHeartbeat)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// Longer than one lease period.
	time.Sleep(400 * time.Millisecond)
	// Another worker must still be refused.
	if err = db.AcquireLease(ctx, project.ID, "RUN-2", time.Now().UTC(), time.Minute); err == nil {
		t.Fatal("the lease was not renewed, so a second worker took the project over")
	}
}

// And releasing it lets the next worker in. A heartbeat that outlives the work
// is a project nobody can ever lease again.
func TestReleasingTheLeaseStopsTheHeartbeat(t *testing.T) {
	ctx, db, project := leaseFixture(t)
	release, err := db.HoldLease(ctx, project.ID, "RUN-1", heldLeaseDuration, heldLeaseHeartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	// Past a lease period with nothing holding it: the next worker gets in.
	time.Sleep(200 * time.Millisecond)
	if err = db.AcquireLease(ctx, project.ID, "RUN-2", time.Now().UTC(), time.Minute); err != nil {
		t.Fatalf("nothing holds this project any more: %v", err)
	}
}

// A generation lease is held the same way, and the generation it reports is
// the one the fence compares against at the end.
func TestAGenerationLeaseIsKeptAndKeepsItsGeneration(t *testing.T) {
	ctx, db, project := leaseFixture(t)
	lease, release, err := db.HoldGenerationLease(ctx, project.ID, "RUN-1", heldLeaseDuration, heldLeaseHeartbeat)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	time.Sleep(400 * time.Millisecond)
	// Renewal is not a change of tenancy, so the generation must not move —
	// a heartbeat that bumped it would make every long run fence itself out.
	if err = db.Fence(ctx, lease); err != nil {
		t.Fatalf("the same worker running on is not a takeover: %v", err)
	}
	current, err := db.CurrentGeneration(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current != lease.Generation {
		t.Fatalf("generation moved under a renewal: held %d, now %d", lease.Generation, current)
	}
}

// A cancel part-way through still ends the tenancy. The heartbeat must not
// paper over a generation bump — that is the one thing the fence is for.
func TestAHeartbeatDoesNotSurviveACancel(t *testing.T) {
	ctx, db, project := leaseFixture(t)
	lease, release, err := db.HoldGenerationLease(ctx, project.ID, "RUN-1", heldLeaseDuration, heldLeaseHeartbeat)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = db.BumpGeneration(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err = db.Fence(ctx, lease); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("the tenancy ended and a late confirmation must be refused: %v", err)
	}
}

// Releasing gives the project up now, not when the lease would have expired.
// Letting it lapse instead makes the next worker wait out a whole period for a
// project nobody is working on.
func TestReleasingGivesTheProjectUpImmediately(t *testing.T) {
	ctx, db, project := leaseFixture(t)
	release, err := db.HoldLease(ctx, project.ID, "RUN-1", time.Hour, heldLeaseHeartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	// An hour-long lease: if release only stopped renewing, this would be
	// refused for the next hour.
	if err = db.AcquireLease(ctx, project.ID, "RUN-2", time.Now().UTC(), time.Minute); err != nil {
		t.Fatalf("the lease was released, so the next worker may have it: %v", err)
	}
}

// The heartbeat is one goroutine per held lease. A worker that holds thousands
// of leases over its life leaks one each time if the release does not join it.
func TestReleasingJoinsTheHeartbeatGoroutine(t *testing.T) {
	ctx, db, project := leaseFixture(t)
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		release, err := db.HoldLease(ctx, project.ID, fmt.Sprintf("RUN-%d", i),
			time.Hour, heldLeaseHeartbeat)
		if err != nil {
			t.Fatal(err)
		}
		if err = release(); err != nil {
			t.Fatal(err)
		}
	}
	// Joined, so the count is back where it started rather than twenty higher.
	// A small margin for the runtime's own goroutines, not for twenty leaks.
	if after := runtime.NumGoroutine(); after > before+3 {
		t.Fatalf("goroutines %d → %d: the release did not join the heartbeat", before, after)
	}
}

// A lease that lapsed before its first renewal is not silently reclaimed.
//
// Renewal failing for a whole period means something went wrong — a locked
// database, a paused container — and in that window another worker may have
// taken the project over. Taking it back without knowing is claiming a tenancy
// that may have ended; the fence is what decides whether the work can still be
// confirmed, and it can only do that if the lapse is real.
func TestALapsedLeaseIsNotReclaimedByTheHeartbeat(t *testing.T) {
	ctx, db, project := leaseFixture(t)
	// The first renewal is due after the lease has already expired.
	release, err := db.HoldLease(ctx, project.ID, "RUN-1", 20*time.Millisecond, 80*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	time.Sleep(250 * time.Millisecond)
	if err = db.AcquireLease(ctx, project.ID, "RUN-2", time.Now().UTC(), time.Minute); err != nil {
		t.Fatalf("the lease lapsed and must not have been taken back: %v", err)
	}
}
