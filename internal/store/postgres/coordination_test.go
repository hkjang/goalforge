package postgres

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// testDSN returns a DSN for a Postgres the test may use. GOALFORGE_TEST_POSTGRES_DSN
// points at an existing server; otherwise a container is started for the
// package and torn down after. These queries coordinate separate machines, so
// they cannot be proven against a fake: SKIP LOCKED and the conditional lease
// upsert are Postgres behaviours, not GoalForge's.
func testDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("GOALFORGE_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	if shared := sharedDSN(t); shared != "" {
		return shared
	}
	t.Skip("no Postgres available; set GOALFORGE_TEST_POSTGRES_DSN or make docker usable")
	return ""
}

var (
	containerOnce sync.Once
	containerDSN  string
)

func sharedDSN(t *testing.T) string {
	t.Helper()
	containerOnce.Do(func() {
		if _, err := exec.LookPath("docker"); err != nil {
			return
		}
		if err := exec.Command("docker", "info").Run(); err != nil {
			return
		}
		name := fmt.Sprintf("goalforge-pg-test-%d", os.Getpid())
		start := exec.Command("docker", "run", "-d", "--rm", "--name", name,
			"-e", "POSTGRES_PASSWORD=gf", "-e", "POSTGRES_USER=gf", "-e", "POSTGRES_DB=gf",
			"-p", "0:5432", "postgres:16-alpine")
		if err := start.Run(); err != nil {
			return
		}
		port, err := exec.Command("docker", "port", name, "5432/tcp").Output()
		if err != nil {
			exec.Command("docker", "rm", "-f", name).Run()
			return
		}
		mapped := string(port)
		if idx := lastColon(mapped); idx >= 0 {
			mapped = trimSpace(mapped[idx+1:])
		}
		dsn := "postgres://gf:gf@127.0.0.1:" + mapped + "/gf?sslmode=disable"
		// The server accepts connections a moment after the container starts.
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			s, openErr := Open(ctx, dsn)
			cancel()
			if openErr == nil {
				s.Close()
				containerDSN = dsn
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if containerDSN == "" {
			exec.Command("docker", "rm", "-f", name).Run()
			return
		}
		cleanupContainer = func() { exec.Command("docker", "rm", "-f", name).Run() }
	})
	return containerDSN
}

var cleanupContainer func()

func TestMain(m *testing.M) {
	code := m.Run()
	if cleanupContainer != nil {
		cleanupContainer()
	}
	os.Exit(code)
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// freshStore opens the store and clears the coordination tables, so one test's
// jobs cannot be claimed by another's worker.
func freshStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err = s.db.ExecContext(ctx, `TRUNCATE scheduler_jobs, process_leases`); err != nil {
		t.Fatal(err)
	}
	return s
}

// The reason Postgres is here at all: two workers on different machines poll
// the same queue, and a job must be handed to exactly one of them. Nothing had
// ever run this query, so the claim was a comment rather than a fact.
func TestOneJobGoesToExactlyOneWorker(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := s.ScheduleJob(ctx, store.SchedulerJob{ProjectID: "P1", Type: "CONTINUE",
		RunAt: now.Add(-time.Minute), IdempotencyKey: "only-one"}); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	var wg sync.WaitGroup
	claimed := make([]string, workers)
	errs := make([]error, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			job, err := s.ClaimDueJob(ctx, now, fmt.Sprintf("worker-%d", i), time.Minute)
			errs[i], claimed[i] = err, job.ID
		}(i)
	}
	wg.Wait()
	winners := 0
	for i := range claimed {
		if errs[i] == nil && claimed[i] != "" {
			winners++
			continue
		}
		if errs[i] != nil && errs[i] != store.ErrNotFound {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
	}
	if winners != 1 {
		t.Fatalf("exactly one worker may hold the job, got %d", winners)
	}
}

// Enqueueing the same intent twice must not create two jobs. Two machines that
// both notice a project needs continuing would otherwise run it twice.
func TestSameIdempotencyKeyEnqueuesOnce(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	first, err := s.ScheduleJob(ctx, store.SchedulerJob{ProjectID: "P1", Type: "CONTINUE",
		RunAt: now, IdempotencyKey: "same-intent"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ScheduleJob(ctx, store.SchedulerJob{ProjectID: "P1", Type: "CONTINUE",
		RunAt: now, IdempotencyKey: "same-intent"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("the same intent produced two jobs: %s and %s", first.ID, second.ID)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_jobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows=%d", count)
	}
}

// A worker that dies holding a job must not strand it. The lease expiring is
// what lets another machine pick the work up.
func TestExpiredJobLeaseIsReclaimed(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := s.ScheduleJob(ctx, store.SchedulerJob{ProjectID: "P1", Type: "CONTINUE",
		RunAt: now.Add(-time.Minute), IdempotencyKey: "abandoned"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDueJob(ctx, now, "worker-that-dies", time.Minute); err != nil {
		t.Fatal(err)
	}
	// Still leased: nobody else may take it.
	if _, err := s.ClaimDueJob(ctx, now.Add(30*time.Second), "worker-b", time.Minute); err != store.ErrNotFound {
		t.Fatalf("a live lease must not be stealable: %v", err)
	}
	// After it expires, it is available again.
	job, err := s.ClaimDueJob(ctx, now.Add(2*time.Minute), "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("an expired lease must be reclaimable: %v", err)
	}
	if job.Owner != "worker-b" || job.Attempts != 2 {
		t.Fatalf("the retry must be recorded: owner=%s attempts=%d", job.Owner, job.Attempts)
	}
}

// Finishing a job requires still holding its lease. A worker that was declared
// dead and superseded must not be able to mark the work done afterwards.
func TestOnlyTheLeaseHolderMayFinishAJob(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := s.ScheduleJob(ctx, store.SchedulerJob{ProjectID: "P1", Type: "CONTINUE",
		RunAt: now.Add(-time.Minute), IdempotencyKey: "ownership"}); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimDueJob(ctx, now, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteJob(ctx, job.ID, "worker-b"); err == nil {
		t.Fatal("a worker that does not hold the lease must not complete the job")
	}
	if err = s.CompleteJob(ctx, job.ID, "worker-a"); err != nil {
		t.Fatalf("the lease holder must be able to complete it: %v", err)
	}
}

// The project lease is what stops two machines running the same project at
// once. Everything about concurrent execution safety rests on it.
func TestOnlyOneMachineHoldsAProjectLease(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.AcquireLease(ctx, "P1", "machine-a", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.AcquireLease(ctx, "P1", "machine-b", now, time.Minute); err == nil {
		t.Fatal("a second machine must not acquire a held lease")
	}
	// The holder may renew.
	if err := s.HeartbeatLease(ctx, "P1", "machine-a", now.Add(10*time.Second), time.Minute); err != nil {
		t.Fatalf("the holder must be able to renew: %v", err)
	}
	// A machine that does not hold it may not renew it.
	if err := s.HeartbeatLease(ctx, "P1", "machine-b", now.Add(11*time.Second), time.Minute); err == nil {
		t.Fatal("a non-holder must not renew someone else's lease")
	}
	// Once it expires, another machine may take over.
	if err := s.AcquireLease(ctx, "P1", "machine-b", now.Add(2*time.Minute), time.Minute); err != nil {
		t.Fatalf("an expired lease must be takeable: %v", err)
	}
	if err := s.ReleaseLease(ctx, "P1", "machine-a"); err == nil {
		t.Fatal("the superseded machine must not be able to release the new holder's lease")
	}
}

// Concurrent acquisition of the same project lease must leave exactly one
// holder, not two that each believe they own it.
func TestConcurrentLeaseAcquisitionHasOneWinner(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	const machines = 8
	var wg sync.WaitGroup
	results := make([]error, machines)
	wg.Add(machines)
	for i := 0; i < machines; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = s.AcquireLease(ctx, "P-race", fmt.Sprintf("machine-%d", i), now, time.Minute)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("exactly one machine may hold the lease, got %d", winners)
	}
}

// A recurring intent must be re-armable once it has finished, and must not
// disturb one that is still on its way. Getting this wrong either strands the
// project (never re-enqueued) or runs it twice.
func TestRecurringJobRevivesOnlyWhenFinished(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	job := store.SchedulerJob{ProjectID: "P1", Type: "CONTINUE", RunAt: now, IdempotencyKey: "continue:P1"}
	first, err := s.ScheduleRecurringJob(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	// Still PENDING: re-enqueueing must not duplicate it.
	again, err := s.ScheduleRecurringJob(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Status != "PENDING" {
		t.Fatalf("a pending job must be left alone: %+v", again)
	}
	// Run it to failure, then re-enqueue: it must come back as PENDING with the
	// stale error cleared.
	claimed, err := s.ClaimDueJob(ctx, now, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FailJob(ctx, claimed.ID, "worker-a", "provider timed out"); err != nil {
		t.Fatal(err)
	}
	revived, err := s.ScheduleRecurringJob(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if revived.Status != "PENDING" {
		t.Fatalf("a finished recurring job must be re-armable: %+v", revived)
	}
	if revived.LastError != "" {
		t.Fatalf("the previous failure must not follow the new attempt: %q", revived.LastError)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_jobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reviving must reuse the row, not add one: rows=%d", count)
	}
}
