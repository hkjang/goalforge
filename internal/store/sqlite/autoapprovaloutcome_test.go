package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func autoApprovalFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.CreateProject(t.Context(), model.Project{ID: "PRJ-1", Name: "p", RepositoryPath: "/r",
		DefaultBranch: "main", Provider: "codex", Model: "sonnet", WIPLimit: 1}); err != nil {
		t.Fatal(err)
	}
	return s, "PRJ-1"
}

// The autonomy loop refuses to approve an item whose previous automatic
// attempt was settled and failed: "whatever stopped it is still there, and a
// second identical attempt spends budget to reach the same place."
//
// Nothing settled them. RecordAutoApprovalOutcome had no caller, so Settled
// stayed false on every record and that guard never fired — the loop was free
// to re-approve a failing item every sweep, spending the day's allowance to
// reach the same place, which is the loop the guard exists to stop.
func TestSettlingAnAutomaticAttemptIsWhatStopsItBeingRetried(t *testing.T) {
	s, projectID := autoApprovalFixture(t)
	ctx := t.Context()
	if err := s.RecordAutoApproval(ctx, AutoApprovalRecord{WorkItemID: "WI-1", ProjectID: projectID,
		StandardID: "T-001", Basis: "기준 T-001", ApprovedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	record, err := s.AutoApprovalFor(ctx, "WI-1")
	if err != nil {
		t.Fatal(err)
	}
	if record.Settled {
		t.Fatal("the attempt has not ended yet")
	}
	if err = s.SettleAutoApproval(ctx, "WI-1", false, "필수 게이트 build_passed 실패"); err != nil {
		t.Fatal(err)
	}
	if record, err = s.AutoApprovalFor(ctx, "WI-1"); err != nil {
		t.Fatal(err)
	}
	if !record.Settled || record.Passed {
		t.Fatalf("the attempt ended and did not pass: %+v", record)
	}
	// The reason is kept whether it passed or failed. A failure with no reason
	// leaves the next reader to work out from scratch what already went wrong.
	if record.Outcome == "" {
		t.Fatal("the reason has to survive")
	}
}

// Settling something nobody auto-approved is refused rather than silently
// updating nothing. A settle that matched no row would look like it worked,
// and the guard it feeds would stay blind for exactly the item it was told
// about.
func TestSettlingAnAttemptNobodyApprovedIsRefused(t *testing.T) {
	s, _ := autoApprovalFixture(t)
	if err := s.SettleAutoApproval(t.Context(), "WI-404", true, "ok"); err == nil {
		t.Fatal("there is no automatic attempt for this item")
	}
}

// A passing attempt is settled too, so the listing can tell an item that
// worked from one still outstanding.
func TestAPassingAttemptIsSettledAsPassed(t *testing.T) {
	s, projectID := autoApprovalFixture(t)
	ctx := t.Context()
	if err := s.RecordAutoApproval(ctx, AutoApprovalRecord{WorkItemID: "WI-1", ProjectID: projectID,
		StandardID: "T-001", Basis: "b", ApprovedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleAutoApproval(ctx, "WI-1", true, "게이트 3건 통과"); err != nil {
		t.Fatal(err)
	}
	records, err := s.AutoApprovals(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !records[0].Settled || !records[0].Passed {
		t.Fatalf("records=%+v", records)
	}
}
