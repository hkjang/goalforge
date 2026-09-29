package sqlite

import (
	"strings"
	"testing"
	"time"
)

// The same event arriving twice must converge on one pass, not two. A worker
// that retries a delivery, or two workers that see the same commit, would
// otherwise each walk the repository and each file the findings.
func TestTheSameEventConvergesOnOneSupplyRun(t *testing.T) {
	ctx, s, project, _ := boardFixture(t)
	run := SupplyRun{ProjectID: project.ID, CommitSHA: "abc123", Trigger: TriggerCommitChanged}
	first, started, err := s.BeginSupplyRun(ctx, run)
	if err != nil || !started {
		t.Fatalf("the first attempt starts the pass: %v %v", started, err)
	}
	second, started, err := s.BeginSupplyRun(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("a retry of the same event must not start a second pass")
	}
	if second.ID != first.ID {
		t.Fatalf("the retry must converge on the same run: %s vs %s", second.ID, first.ID)
	}
	// A different commit is a different event.
	if _, started, err = s.BeginSupplyRun(ctx, SupplyRun{ProjectID: project.ID, CommitSHA: "def456",
		Trigger: TriggerCommitChanged}); err != nil || !started {
		t.Fatalf("a new commit is a new event: %v %v", started, err)
	}
}

// The trap in every idempotency key. A pass that crashed half way leaves a
// RUNNING record, and a key that only asks "has this been seen" will refuse
// every retry forever — the one failure mode the mechanism was added to
// prevent.
func TestACrashedPassDoesNotBlockRetriesForever(t *testing.T) {
	ctx, s, project, _ := boardFixture(t)
	run := SupplyRun{ProjectID: project.ID, CommitSHA: "abc123", Trigger: TriggerScheduled}
	if _, _, err := s.BeginSupplyRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	// Still in flight: a second worker must stay out of the way.
	if _, started, err := s.BeginSupplyRun(ctx, run); err != nil || started {
		t.Fatalf("a pass still running must not be restarted: %v %v", started, err)
	}
	// Aged past the point where a live pass is plausible, it is abandoned.
	if err := s.expireSupplyRunForTest(ctx, run, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	resumed, started, err := s.BeginSupplyRun(ctx, run)
	if err != nil || !started {
		t.Fatalf("an abandoned pass must be retryable: %v %v", started, err)
	}
	if resumed.Status != SupplyRunning {
		t.Fatalf("status=%s", resumed.Status)
	}
	// A completed pass is a different matter: it finished, and repeating it
	// would file its findings a second time.
	if err = s.FinishSupplyRun(ctx, run.Key(), SupplyRunCompleted, "", SupplyCounts{Filed: 2}); err != nil {
		t.Fatal(err)
	}
	if _, started, err = s.BeginSupplyRun(ctx, run); err != nil || started {
		t.Fatalf("a completed pass must not be repeated: %v %v", started, err)
	}
}

// A pass that failed is not a pass that succeeded. Refusing to retry it would
// leave the project unassessed because of one bad night.
func TestAFailedPassIsRetryable(t *testing.T) {
	ctx, s, project, _ := boardFixture(t)
	run := SupplyRun{ProjectID: project.ID, CommitSHA: "abc123", Trigger: TriggerScheduled}
	if _, _, err := s.BeginSupplyRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishSupplyRun(ctx, run.Key(), SupplyRunFailed, "저장소를 읽지 못했습니다", SupplyCounts{}); err != nil {
		t.Fatal(err)
	}
	if _, started, err := s.BeginSupplyRun(ctx, run); err != nil || !started {
		t.Fatalf("a failed pass must be retryable: %v %v", started, err)
	}
}

// What the pass did is recorded, so a report can say whether anything changed
// without walking the board.
func TestASupplyRunRecordsWhatItDid(t *testing.T) {
	ctx, s, project, _ := boardFixture(t)
	run := SupplyRun{ProjectID: project.ID, CommitSHA: "abc123", Trigger: TriggerBacklogLow}
	if _, _, err := s.BeginSupplyRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishSupplyRun(ctx, run.Key(), SupplyRunCompleted, "",
		SupplyCounts{Filed: 2, AlreadyFiled: 3, Deferred: 1, Unchecked: 24}); err != nil {
		t.Fatal(err)
	}
	last, err := s.LastSupplyRun(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last.Filed != 2 || last.AlreadyFiled != 3 || last.Deferred != 1 || last.Unchecked != 24 {
		t.Fatalf("counts=%+v", last)
	}
	if last.Trigger != TriggerBacklogLow || last.CommitSHA != "abc123" {
		t.Fatalf("the reason the pass ran is part of what it did: %+v", last)
	}
	if last.EndedAt.IsZero() {
		t.Fatal("a finished pass has an end")
	}
}

// The key is the event, and an event is a project, a commit and a reason. Two
// of the three matching is a different event.
func TestTheSupplyKeyIsTheWholeEvent(t *testing.T) {
	base := SupplyRun{ProjectID: "P-1", CommitSHA: "abc", Trigger: TriggerScheduled}
	for _, other := range []SupplyRun{
		{ProjectID: "P-2", CommitSHA: "abc", Trigger: TriggerScheduled},
		{ProjectID: "P-1", CommitSHA: "def", Trigger: TriggerScheduled},
		{ProjectID: "P-1", CommitSHA: "abc", Trigger: TriggerBacklogLow},
	} {
		if other.Key() == base.Key() {
			t.Fatalf("%+v must be a different event", other)
		}
	}
	if strings.TrimSpace(base.Key()) == "" {
		t.Fatal("the key must not be empty")
	}
}
