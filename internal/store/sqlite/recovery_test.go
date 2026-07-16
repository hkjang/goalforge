package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func recoveryStore(t *testing.T) (context.Context, *Store, model.Project) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	p := model.Project{ID: "PRJ-RECOVERY", Name: "demo", RepositoryPath: "/recovery", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	return ctx, s, p
}

func TestCheckpointRoundTrip(t *testing.T) {
	ctx, s, p := recoveryStore(t)
	defer s.Close()
	created, err := s.CreateCheckpoint(ctx, Checkpoint{ProjectID: p.ID, GoalVersion: 2, Provider: "codex", SessionID: "thr-1", CommitSHA: "abc", Branch: "main", DirtyFiles: []string{"a.go", "b.go"}, DirtyFingerprint: "fingerprint-1", NextAction: "resume work", RemainingSteps: "run tests"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestCheckpoint(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.SessionID != "thr-1" || len(got.DirtyFiles) != 2 || got.DirtyFingerprint != "fingerprint-1" || got.NextAction != "resume work" {
		t.Fatalf("checkpoint=%+v", got)
	}
	// Every DB checkpoint keeps a human-readable CONTINUITY.md companion in
	// the state directory, never inside the repository working tree.
	continuity, err := os.ReadFile(s.ContinuityPath(p.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{created.ID, "resume work", "run tests", "a.go, b.go", "thr-1"} {
		if !strings.Contains(string(continuity), expected) {
			t.Fatalf("continuity missing %q:\n%s", expected, continuity)
		}
	}
	if strings.HasPrefix(s.ContinuityPath(p.ID), p.RepositoryPath) {
		t.Fatal("continuity file must not live inside the repository")
	}
}

func TestScheduleJobIsIdempotentAndClaimedOnce(t *testing.T) {
	ctx, s, p := recoveryStore(t)
	defer s.Close()
	now := time.Now().UTC()
	first, err := s.ScheduleJob(ctx, SchedulerJob{ProjectID: p.ID, Type: "RESUME", RunAt: now, IdempotencyKey: "resume:1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ScheduleJob(ctx, SchedulerJob{ProjectID: p.ID, Type: "RESUME", RunAt: now.Add(time.Hour), IdempotencyKey: "resume:1"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !second.RunAt.Equal(first.RunAt) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	claimed, err := s.ClaimDueJob(ctx, now.Add(time.Second), "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != first.ID || claimed.Attempts != 1 {
		t.Fatalf("claimed=%+v", claimed)
	}
	if _, err = s.ClaimDueJob(ctx, now.Add(time.Second), "worker-2", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second claim err=%v", err)
	}
	if err = s.CompleteJob(ctx, claimed.ID, "worker-2"); err == nil {
		t.Fatal("non-owner completed job")
	}
	if err = s.CompleteJob(ctx, claimed.ID, "worker-1"); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredJobLeaseIsRecovered(t *testing.T) {
	ctx, s, p := recoveryStore(t)
	defer s.Close()
	now := time.Now().UTC()
	job, err := s.ScheduleJob(ctx, SchedulerJob{ProjectID: p.ID, Type: "RESUME", RunAt: now, IdempotencyKey: "resume:expired"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimDueJob(ctx, now, "dead-worker", time.Second); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ClaimDueJob(ctx, now.Add(2*time.Second), "new-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != job.ID || recovered.Attempts != 2 || recovered.Owner != "new-worker" {
		t.Fatalf("recovered=%+v", recovered)
	}
}

func TestProjectLeaseOwnershipAndExpiry(t *testing.T) {
	ctx, s, p := recoveryStore(t)
	defer s.Close()
	now := time.Now().UTC()
	if err := s.AcquireLease(ctx, p.ID, "one", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.AcquireLease(ctx, p.ID, "two", now, time.Minute); err == nil {
		t.Fatal("second owner acquired live lease")
	}
	if err := s.HeartbeatLease(ctx, p.ID, "two", now, time.Minute); err == nil {
		t.Fatal("non-owner heartbeat succeeded")
	}
	if err := s.AcquireLease(ctx, p.ID, "two", now.Add(2*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(ctx, p.ID, "one"); err == nil {
		t.Fatal("old owner released lease")
	}
	if err := s.ReleaseLease(ctx, p.ID, "two"); err != nil {
		t.Fatal(err)
	}
}

func TestEnterQuotaWaitPersistsContinuityAndResumeAtomically(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	p := model.Project{ID: "P-WAIT", Name: "demo", RepositoryPath: "/wait", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionProjectState(ctx, p.ID, "CREATED", "READY"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reset := now.Add(time.Hour)
	resume := reset.Add(time.Minute)
	err = s.EnterQuotaWait(ctx, QuotaWindow{Provider: "codex", AccountID: "personal", LimitType: "session", Status: "exhausted", UsedPercent: 100, DetectedAt: now, QuotaResetAt: &reset, ResumeAt: &resume, Source: "app_server", Confidence: "high"}, Checkpoint{ProjectID: p.ID, Provider: "codex", SessionID: "thr-1", GoalVersion: 1, NextAction: "continue WORK-1"}, SchedulerJob{ProjectID: p.ID, Type: "RESUME", IdempotencyKey: "quota:" + p.ID, RunAt: resume})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gotProject, err := s.ProjectByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotProject.State != "WAITING_QUOTA" {
		t.Fatalf("state=%s", gotProject.State)
	}
	checkpoint, err := s.LatestCheckpoint(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.SessionID != "thr-1" || checkpoint.NextAction != "continue WORK-1" {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
	job, err := s.ClaimDueJob(ctx, resume, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if job.Type != "RESUME" || job.ProjectID != p.ID {
		t.Fatalf("job=%+v", job)
	}
}
