package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/policy"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// fakeLoader records what the UI asked the store to do, so the tests are about
// the interface's behaviour rather than a database's.
type fakeLoader struct {
	snapshot  Snapshot
	loadErr   error
	approved  []string
	rejected  []string
	decideErr error
	loads     int
}

func (f *fakeLoader) Load(_ context.Context, selected int) (Snapshot, error) {
	f.loads++
	snapshot := f.snapshot
	snapshot.Selected = selected
	return snapshot, f.loadErr
}
func (f *fakeLoader) Approve(_ context.Context, _, approvalID string) error {
	if f.decideErr != nil {
		return f.decideErr
	}
	f.approved = append(f.approved, approvalID)
	return nil
}
func (f *fakeLoader) Reject(_ context.Context, _, approvalID, _, _ string) error {
	if f.decideErr != nil {
		return f.decideErr
	}
	f.rejected = append(f.rejected, approvalID)
	return nil
}

func loaded(t *testing.T, loader *fakeLoader) Model {
	t.Helper()
	m := New(loader, time.Minute)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)
	snapshot, err := loader.Load(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ = m.Update(snapshotMsg{snapshot: snapshot})
	return updated.(Model)
}

func press(t *testing.T, m Model, key string) (Model, tea.Cmd) {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "up", "down", "left", "right", "tab", "esc":
		msg = tea.KeyMsg{Type: map[string]tea.KeyType{"up": tea.KeyUp, "down": tea.KeyDown,
			"left": tea.KeyLeft, "right": tea.KeyRight, "tab": tea.KeyTab, "esc": tea.KeyEsc}[key]}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}

func approvalSnapshot() Snapshot {
	return Snapshot{
		TakenAt:  time.Now(),
		Projects: []ProjectRow{{Project: model.Project{ID: "PRJ-1", Name: "demo", State: "READY"}}},
		Approvals: []store.PendingApproval{
			{Approval: store.Approval{ID: "APR-1", ProjectID: "PRJ-1", ActionType: store.ApprovalMergeBranch, Reason: "첫 번째"}},
			{Approval: store.Approval{ID: "APR-2", ProjectID: "PRJ-1", ActionType: store.ApprovalProtectedFiles, Reason: "두 번째"}},
		},
	}
}

// The boundary this screen must not become a way around. An implementation
// session is refused `goalforge approval approve`; opening a TUI is not a
// permission, so it is refused here too — and the refusal happens before the
// store is touched.
func TestImplementationSessionCannotApproveFromTheTUI(t *testing.T) {
	t.Setenv(policy.EnvRole, policy.RoleImplementation)
	loader := &fakeLoader{snapshot: approvalSnapshot()}
	m := loaded(t, loader)
	m, _ = press(t, m, "4")
	m, cmd := press(t, m, "a")
	if len(loader.approved) != 0 {
		t.Fatalf("the store must not be reached at all: %v", loader.approved)
	}
	if cmd != nil {
		t.Fatal("no command may be issued for a refused action")
	}
	if !m.state.MessageIsError || m.state.Message == "" {
		t.Fatalf("the refusal must be shown: %+v", m.state)
	}
	// Rejecting is an authority decision too, and refusing one while allowing
	// the other would be a boundary with a hole in it.
	m, _ = press(t, m, "x")
	if len(loader.rejected) != 0 {
		t.Fatalf("rejection is also an authority decision: %v", loader.rejected)
	}
}

// The operator can decide, or the boundary would just be a wall.
func TestOperatorCanApproveFromTheTUI(t *testing.T) {
	t.Setenv(policy.EnvRole, "")
	loader := &fakeLoader{snapshot: approvalSnapshot()}
	m := loaded(t, loader)
	m, _ = press(t, m, "4")
	_, cmd := press(t, m, "a")
	if cmd == nil {
		t.Fatal("an operator's approval must be carried out")
	}
	msg, ok := cmd().(actionMsg)
	if !ok {
		t.Fatalf("unexpected message %T", cmd())
	}
	if msg.err != nil {
		t.Fatalf("err=%v", msg.err)
	}
	if len(loader.approved) != 1 || loader.approved[0] != "APR-1" {
		t.Fatalf("the selected approval must be the one decided: %v", loader.approved)
	}
}

// The decision must land on the row the cursor is on, not the first one.
func TestDecisionAppliesToTheSelectedRow(t *testing.T) {
	t.Setenv(policy.EnvRole, "")
	loader := &fakeLoader{snapshot: approvalSnapshot()}
	m := loaded(t, loader)
	m, _ = press(t, m, "4")
	m, _ = press(t, m, "down")
	_, cmd := press(t, m, "x")
	if cmd == nil {
		t.Fatal("no command issued")
	}
	cmd()
	if len(loader.rejected) != 1 || loader.rejected[0] != "APR-2" {
		t.Fatalf("rejected=%v, want the second row", loader.rejected)
	}
}

// A cursor that can leave its list selects something that is not there.
func TestCursorStaysInsideItsList(t *testing.T) {
	loader := &fakeLoader{snapshot: approvalSnapshot()}
	m := loaded(t, loader)
	m, _ = press(t, m, "4")
	for i := 0; i < 10; i++ {
		m, _ = press(t, m, "down")
	}
	if m.state.Cursor != 1 {
		t.Fatalf("cursor=%d, want the last row", m.state.Cursor)
	}
	for i := 0; i < 10; i++ {
		m, _ = press(t, m, "up")
	}
	if m.state.Cursor != 0 {
		t.Fatalf("cursor=%d, want the first row", m.state.Cursor)
	}
	// Switching to a tab with no rows must not leave the cursor pointing at a
	// row that tab does not have.
	m, _ = press(t, m, "1")
	if m.state.Cursor != 0 {
		t.Fatalf("cursor=%d after switching tabs", m.state.Cursor)
	}
}

// Pressing approve outside the approvals tab, or with nothing to approve, must
// do nothing rather than act on whatever the cursor happens to index.
func TestApproveIsInertWhereThereIsNothingToApprove(t *testing.T) {
	t.Setenv(policy.EnvRole, "")
	loader := &fakeLoader{snapshot: approvalSnapshot()}
	m := loaded(t, loader)
	if _, cmd := press(t, m, "a"); cmd != nil {
		t.Fatal("approve must be inert on the overview tab")
	}
	empty := &fakeLoader{snapshot: Snapshot{TakenAt: time.Now(),
		Projects: []ProjectRow{{Project: model.Project{ID: "PRJ-1", Name: "demo"}}}}}
	m = loaded(t, empty)
	m, _ = press(t, m, "4")
	if _, cmd := press(t, m, "a"); cmd != nil {
		t.Fatal("approve must be inert with an empty inbox")
	}
}

// A failed load must say so and leave the last good screen up, rather than
// blanking it: an empty dashboard reads as "nothing is happening".
func TestLoadFailureKeepsTheLastGoodScreen(t *testing.T) {
	loader := &fakeLoader{snapshot: approvalSnapshot()}
	m := loaded(t, loader)
	updated, _ := m.Update(snapshotMsg{err: errors.New("database is locked")})
	m = updated.(Model)
	if len(m.snapshot.Approvals) != 2 {
		t.Fatal("the last good snapshot must survive a failed refresh")
	}
	if !m.state.MessageIsError || !strings.Contains(m.state.Message, "database is locked") {
		t.Fatalf("the failure must be reported: %+v", m.state)
	}
}

// A decision that the store refuses has to surface, not be swallowed into a
// screen that looks like it worked.
func TestStoreRefusalIsShown(t *testing.T) {
	t.Setenv(policy.EnvRole, "")
	loader := &fakeLoader{snapshot: approvalSnapshot(), decideErr: errors.New("approval is not pending")}
	m := loaded(t, loader)
	m, _ = press(t, m, "4")
	_, cmd := press(t, m, "a")
	updated, _ := m.Update(cmd())
	m = updated.(Model)
	if !m.state.MessageIsError || !strings.Contains(m.state.Message, "not pending") {
		t.Fatalf("the store's refusal must be shown: %+v", m.state)
	}
}

// Quitting must always work, including while a refusal is on screen.
func TestQuitAlwaysWorks(t *testing.T) {
	loader := &fakeLoader{snapshot: approvalSnapshot()}
	for _, key := range []string{"q", "esc"} {
		m := loaded(t, loader)
		updated, cmd := press(t, m, key)
		if cmd == nil {
			t.Fatalf("%s must quit", key)
		}
		if updated.View() != "" {
			t.Fatalf("%s must leave a clean screen", key)
		}
	}
}

// The reported bug: on a one-project install, every arrow key was inert on the
// tab the user lands on. Up and down did nothing because the overview counted
// zero rows, and left and right did nothing because there was no other project
// to switch to.
func TestArrowsMoveTheProjectListOnTheOverview(t *testing.T) {
	loader := &fakeLoader{snapshot: Snapshot{TakenAt: time.Now(), Projects: []ProjectRow{
		{Project: model.Project{ID: "PRJ-1", Name: "one"}},
		{Project: model.Project{ID: "PRJ-2", Name: "two"}},
		{Project: model.Project{ID: "PRJ-3", Name: "three"}},
	}}}
	m := loaded(t, loader)
	if m.state.Tab != TabOverview {
		t.Fatalf("the overview is where a user lands: %d", m.state.Tab)
	}
	m, cmd := press(t, m, "down")
	if m.state.Cursor != 1 {
		t.Fatalf("down must move the visible list: cursor=%d", m.state.Cursor)
	}
	if cmd == nil {
		t.Fatal("moving to another project must reload what the other tabs describe")
	}
	if m.snapshot.Selected != 1 {
		t.Fatalf("the cursor is the selection on the overview: selected=%d", m.snapshot.Selected)
	}
	m, _ = press(t, m, "down")
	m, _ = press(t, m, "up")
	if m.state.Cursor != 1 || m.snapshot.Selected != 1 {
		t.Fatalf("up must come back: cursor=%d selected=%d", m.state.Cursor, m.snapshot.Selected)
	}
}

// A single project is the common case and the one that made every key look
// broken: the arrows must still be handled, they just have nowhere to go.
func TestArrowsAreHarmlessWithOneProject(t *testing.T) {
	loader := &fakeLoader{snapshot: Snapshot{TakenAt: time.Now(),
		Projects: []ProjectRow{{Project: model.Project{ID: "PRJ-1", Name: "only"}}}}}
	m := loaded(t, loader)
	for _, key := range []string{"up", "down", "left", "right"} {
		m, _ = press(t, m, key)
		if m.state.Cursor != 0 || m.snapshot.Selected != 0 {
			t.Fatalf("%s moved past the only project: cursor=%d selected=%d", key, m.state.Cursor, m.snapshot.Selected)
		}
	}
}

// Page and home/end keys exist so a long backlog is navigable without holding
// down an arrow.
func TestPageAndEdgeKeysMove(t *testing.T) {
	rows := make([]WorkRow, 60)
	for i := range rows {
		rows[i] = WorkRow{Item: model.WorkItem{ID: "W", Title: "item"}}
	}
	loader := &fakeLoader{snapshot: Snapshot{TakenAt: time.Now(),
		Projects: []ProjectRow{{Project: model.Project{ID: "PRJ-1", Name: "p"}}}, Work: rows}}
	m := loaded(t, loader)
	m, _ = press(t, m, "3")
	m, _ = press(t, m, "end")
	if m.state.Cursor != 59 {
		t.Fatalf("end must reach the last row: %d", m.state.Cursor)
	}
	m, _ = press(t, m, "home")
	if m.state.Cursor != 0 {
		t.Fatalf("home must reach the first row: %d", m.state.Cursor)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = updated.(Model)
	if m.state.Cursor == 0 {
		t.Fatal("page down must move")
	}
}

// The footer is one line and truncates on a narrow terminal, which is where
// someone is most likely to be looking for the keys.
func TestHelpIsReachableAndDismissable(t *testing.T) {
	loader := &fakeLoader{snapshot: approvalSnapshot()}
	m := loaded(t, loader)
	m, _ = press(t, m, "?")
	if !m.state.ShowHelp {
		t.Fatal("? must open help")
	}
	rendered := m.View()
	for _, want := range []string{"↑ ↓", "← →", "PgUp", "승인 탭"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("help must list %q:\n%s", want, rendered)
		}
	}
	m, _ = press(t, m, "?")
	if m.state.ShowHelp {
		t.Fatal("? must close help")
	}
}
