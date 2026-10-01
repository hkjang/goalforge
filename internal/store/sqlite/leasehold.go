package sqlite

import (
	"context"
	"sync"
	"time"
)

// The lease is what stops two workers running one project at once. It was
// taken for a fixed period and never renewed, so a run that took longer than
// the period lost it — not because the worker died, but because nobody renewed
// it. Another worker could then take the project over while the first was
// still writing to the same working tree, and the fence refused the first
// one's result at the end for a tenancy that ended through neglect.
//
// HeartbeatLease was written for exactly this and had no caller.

// defaultHeartbeatFraction is how often a held lease is renewed, as a fraction
// of its duration.
//
// A third rather than a half: one missed renewal — a slow query, a paused
// container — still leaves a whole period to recover in. Renewing at half the
// period means one miss is already the deadline.
const defaultHeartbeatFraction = 3

// holdLease takes the project lease and keeps it renewed until released.
//
// The returned function stops renewing and releases the lease. It is safe to
// call more than once so a deferred release and an explicit one cannot
// double-release.
func (s *Store) HoldLease(ctx context.Context, projectID, owner string, duration, interval time.Duration) (func() error, error) {
	if err := s.AcquireLease(ctx, projectID, owner, time.Now().UTC(), duration); err != nil {
		return nil, err
	}
	return s.releaser(projectID, owner, s.beat(ctx, projectID, owner, duration, interval)), nil
}

// holdGenerationLease is holdLease for the path that fences its writes, and
// returns the lease so the fence has something to compare against.
func (s *Store) HoldGenerationLease(ctx context.Context, projectID, owner string, duration, interval time.Duration) (Lease, func() error, error) {
	lease, err := s.AcquireGenerationLease(ctx, projectID, owner, time.Now().UTC(), duration)
	if err != nil {
		return lease, nil, err
	}
	return lease, s.releaser(projectID, owner, s.beat(ctx, projectID, owner, duration, interval)), nil
}

// beat renews the lease on a ticker until it is told to stop.
func (s *Store) beat(ctx context.Context, projectID, owner string, duration, interval time.Duration) func() {
	if interval <= 0 {
		interval = duration / defaultHeartbeatFraction
	}
	if interval <= 0 {
		// No lease duration to renew against. Nothing to do, and starting a
		// ticker on a zero interval would spin.
		return func() {}
	}
	// Detached from the caller's context deliberately. The renewal has to
	// outlive a cancelled request long enough for the release to run; the
	// release is what ends it.
	beatCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-beatCtx.Done():
				return
			case <-ticker.C:
				// A failed renewal is not reported here. The lease is already
				// lost or taken over by the time this fails, and the fence is
				// what refuses the write — raising it from a goroutine would
				// race the real error the caller is about to return.
				if err := s.HeartbeatLease(beatCtx, projectID, owner,
					time.Now().UTC(), duration); err != nil {
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		// Waited for, so release is a synchronisation point and no goroutine
		// outlives the store it holds. Not for correctness of the lease: a
		// renewal that lands after the row is deleted updates nothing, and one
		// that lands after another worker took over does not match its owner.
		<-done
	}
}

// releaser stops the heartbeat and gives the project up, once.
func (s *Store) releaser(projectID, owner string, stop func()) func() error {
	var once sync.Once
	var err error
	return func() error {
		once.Do(func() {
			stop()
			// WithoutCancel: a cancelled request must still give the project
			// up, or the next worker waits out the whole lease period for
			// nothing.
			err = s.ReleaseLease(context.Background(), projectID, owner)
		})
		return err
	}
}
