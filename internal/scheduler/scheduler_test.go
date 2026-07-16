package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func setup(t *testing.T) (context.Context, *store.Store, model.Project) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	p := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/scheduler", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	return ctx, s, p
}

func TestRunOneCompletesExactlyOnce(t *testing.T) {
	ctx, s, p := setup(t)
	defer s.Close()
	now := time.Now().UTC()
	if _, err := s.ScheduleJob(ctx, store.SchedulerJob{ProjectID: p.ID, Type: "RESUME", RunAt: now, IdempotencyKey: "one"}); err != nil {
		t.Fatal(err)
	}
	scheduler, _ := New(s, "worker", time.Minute)
	calls := 0
	if err := scheduler.Handle("RESUME", func(context.Context, store.SchedulerJob) (Outcome, error) { calls++; return Outcome{}, nil }); err != nil {
		t.Fatal(err)
	}
	ran, err := scheduler.RunOne(ctx, now)
	if err != nil || !ran {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	ran, err = scheduler.RunOne(ctx, now)
	if err != nil || ran || calls != 1 {
		t.Fatalf("ran=%v calls=%d err=%v", ran, calls, err)
	}
}

func TestHandlerCanRescheduleWithoutBusyLoop(t *testing.T) {
	ctx, s, p := setup(t)
	defer s.Close()
	now := time.Now().UTC()
	if _, err := s.ScheduleJob(ctx, store.SchedulerJob{ProjectID: p.ID, Type: "RESUME", RunAt: now, IdempotencyKey: "retry"}); err != nil {
		t.Fatal(err)
	}
	scheduler, _ := New(s, "worker", time.Minute)
	later := now.Add(time.Hour)
	if err := scheduler.Handle("RESUME", func(context.Context, store.SchedulerJob) (Outcome, error) {
		return Outcome{RescheduleAt: &later}, errors.New("quota still exhausted")
	}); err != nil {
		t.Fatal(err)
	}
	ran, err := scheduler.RunOne(ctx, now)
	if !ran || err == nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	ran, err = scheduler.RunOne(ctx, now.Add(time.Minute))
	if err != nil || ran {
		t.Fatalf("job retried too early: ran=%v err=%v", ran, err)
	}
	claimed, err := s.ClaimDueJob(ctx, later, "other", time.Minute)
	if err != nil || claimed.Attempts != 2 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
}
