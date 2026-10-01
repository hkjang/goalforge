package sqlite

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/rrsi"
)

func changeFixture(t *testing.T) (*Store, string) {
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

func change(field, from, to string) rrsi.Change {
	return rrsi.Change{Field: field, From: from, To: to}
}

// The rule the whole thing rests on. A change applied and never judged is
// worse than no change, and a change left in place after failing to show an
// improvement is the configuration drifting on its failures.
func TestAChangeThatDidNotHelpIsPutBack(t *testing.T) {
	s, projectID := changeFixture(t)
	ctx := t.Context()
	applied, err := s.ApplyChange(ctx, projectID, 1, "concurrency", change("wip_limit", "1", "3"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if project.WIPLimit != 3 {
		t.Fatalf("the change must actually take effect: %d", project.WIPLimit)
	}
	settled, err := s.SettleChange(ctx, applied.ID, Settlement{Verdict: rrsi.VerdictNotShown, Detail: "성공률이 오르지 않았습니다"})
	if err != nil {
		t.Fatal(err)
	}
	if !settled.Reverted {
		t.Fatal("a verdict that did not support the change must put it back")
	}
	if project, err = s.ProjectByID(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if project.WIPLimit != 1 {
		t.Fatalf("the setting must be back where it was: %d", project.WIPLimit)
	}
	// The row is kept. A reverted change that left no trace is one the next
	// round will cheerfully repeat.
	changes, err := s.AppliedChanges(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || !changes[0].Reverted {
		t.Fatalf("changes=%+v", changes)
	}
}

// A change the measurement supported stays.
func TestAChangeThatHelpedStays(t *testing.T) {
	s, projectID := changeFixture(t)
	ctx := t.Context()
	applied, err := s.ApplyChange(ctx, projectID, 1, "concurrency", change("wip_limit", "1", "2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SettleChange(ctx, applied.ID, Settlement{Verdict: rrsi.VerdictBetter, Detail: "성공률 12%p"}); err != nil {
		t.Fatal(err)
	}
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if project.WIPLimit != 2 {
		t.Fatalf("a change that helped stays: %d", project.WIPLimit)
	}
}

// Two changes applied before either is measured make the measurement
// unattributable: the score moved, and nothing says which of them moved it.
func TestOnlyOneChangeIsOutstandingAtATime(t *testing.T) {
	s, projectID := changeFixture(t)
	ctx := t.Context()
	applied, err := s.ApplyChange(ctx, projectID, 1, "concurrency", change("wip_limit", "1", "2"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyChange(ctx, projectID, 1, "budget", change("daily_run_limit", "0", "50"))
	if !errors.Is(err, ErrChangeOutstanding) {
		t.Fatalf("a second change before the first is judged must be refused: %v", err)
	}
	// Once the first is settled the next may go.
	if _, err = s.SettleChange(ctx, applied.ID, Settlement{Verdict: rrsi.VerdictBetter, Detail: "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyChange(ctx, projectID, 2, "budget", change("daily_run_limit", "0", "50")); err != nil {
		t.Fatal(err)
	}
}

// A proposer's idea of the current value is a claim; the database holds the
// fact. Reverting to a wrong "from" is worse than not reverting.
func TestAStaleFromValueIsRefused(t *testing.T) {
	s, projectID := changeFixture(t)
	_, err := s.ApplyChange(t.Context(), projectID, 1, "concurrency", change("wip_limit", "5", "3"))
	if err == nil {
		t.Fatal("the proposal says it is changing from 5 and it is 1")
	}
	project, projErr := s.ProjectByID(t.Context(), projectID)
	if projErr != nil {
		t.Fatal(projErr)
	}
	if project.WIPLimit != 1 {
		t.Fatalf("nothing must have been changed: %d", project.WIPLimit)
	}
}

// A change to something nobody enumerated cannot be reverted: there is no code
// that knows how to put it back.
func TestOnlyEnumeratedSettingsCanBeChanged(t *testing.T) {
	s, projectID := changeFixture(t)
	for _, field := range []string{"prompt_template", "repair_policy", "goal"} {
		if _, err := s.ApplyChange(t.Context(), projectID, 1, "", rrsi.Change{Field: field,
			From: "a", To: "b"}); err == nil {
			t.Fatalf("%q must not be automatically changeable", field)
		}
	}
}

// A change filed under the wrong component is attributed to the wrong
// component, and the history that decides what to try next is built from
// exactly that attribution.
func TestAChangeMustMatchItsComponent(t *testing.T) {
	s, projectID := changeFixture(t)
	_, err := s.ApplyChange(t.Context(), projectID, 1, "prompt", change("wip_limit", "1", "2"))
	if err == nil {
		t.Fatal("wip_limit is a concurrency setting, not a prompt one")
	}
}

// A change with no before value cannot be undone, and one that changes nothing
// spends a round measuring the configuration against itself.
func TestAChangeThatCannotBeUndoneOrChangesNothingIsRefused(t *testing.T) {
	s, projectID := changeFixture(t)
	ctx := t.Context()
	if _, err := s.ApplyChange(ctx, projectID, 1, "concurrency", rrsi.Change{Field: "wip_limit",
		To: "3"}); err == nil {
		t.Fatal("no before value means it cannot be put back")
	}
	if _, err := s.ApplyChange(ctx, projectID, 1, "concurrency", change("wip_limit", "1", "1")); err == nil {
		t.Fatal("a change that changes nothing is not a change")
	}
	if _, err := s.ApplyChange(ctx, projectID, 1, "concurrency", change("wip_limit", "1", "둘")); err == nil {
		t.Fatal("a non-numeric concurrency limit must be refused")
	}
}
