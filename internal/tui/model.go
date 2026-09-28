package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/goalforge/goalforge/internal/policy"
)

// UIState is everything the screen needs that is not data: where the cursor
// is, which tab is showing, how big the terminal is, and what the last action
// had to say. Keeping it apart from the snapshot is what lets a view be a pure
// function of (data, cursor).
type UIState struct {
	Tab            int
	Cursor         int
	Width, Height  int
	Message        string
	MessageIsError bool
}

// Model drives the terminal UI.
type Model struct {
	loader   Loader
	snapshot Snapshot
	state    UIState
	// refreshEvery keeps a long-running screen honest: a dashboard showing a
	// run that finished ten minutes ago is worse than no dashboard.
	refreshEvery time.Duration
	quitting     bool
}

// New builds the model. The terminal size is filled in by the first resize
// message, which Bubble Tea sends before the first render.
func New(loader Loader, refreshEvery time.Duration) Model {
	if refreshEvery <= 0 {
		refreshEvery = 5 * time.Second
	}
	return Model{loader: loader, refreshEvery: refreshEvery,
		state: UIState{Width: 80, Height: 24, Message: "불러오는 중…"}}
}

type snapshotMsg struct {
	snapshot Snapshot
	err      error
}
type tickMsg time.Time
type actionMsg struct {
	message string
	err     error
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.load(), m.tick()) }

func (m Model) load() tea.Cmd {
	loader, selected := m.loader, m.snapshot.Selected
	return func() tea.Msg {
		// The load is bounded so a wedged database cannot freeze the screen
		// with no way to tell why.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snapshot, err := loader.Load(ctx, selected)
		return snapshotMsg{snapshot: snapshot, err: err}
	}
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(m.refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.state.Width, m.state.Height = msg.Width, msg.Height
		return m, nil
	case snapshotMsg:
		if msg.err != nil {
			m.state.Message, m.state.MessageIsError = "불러오기 실패: "+msg.err.Error(), true
			return m, nil
		}
		m.snapshot = msg.snapshot
		m.state.Message, m.state.MessageIsError = "", false
		m.state.Cursor = clampCursor(m.state.Cursor, m.rowCount())
		return m, nil
	case actionMsg:
		if msg.err != nil {
			m.state.Message, m.state.MessageIsError = msg.err.Error(), true
			return m, nil
		}
		m.state.Message, m.state.MessageIsError = msg.message, false
		return m, m.load()
	case tickMsg:
		return m, tea.Batch(m.load(), m.tick())
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		m.quitting = true
		return m, tea.Quit
	case "1", "2", "3", "4", "5":
		m.state.Tab = int(msg.String()[0] - '1')
		m.state.Cursor = 0
		m.state.Message = ""
		return m, nil
	case "tab":
		m.state.Tab, m.state.Cursor = (m.state.Tab+1)%tabCount, 0
		return m, nil
	case "shift+tab":
		m.state.Tab, m.state.Cursor = (m.state.Tab+tabCount-1)%tabCount, 0
		return m, nil
	case "up", "k":
		m.state.Cursor = clampCursor(m.state.Cursor-1, m.rowCount())
		return m, nil
	case "down", "j":
		m.state.Cursor = clampCursor(m.state.Cursor+1, m.rowCount())
		return m, nil
	case "left", "h":
		return m.selectProject(m.snapshot.Selected - 1)
	case "right", "l":
		return m.selectProject(m.snapshot.Selected + 1)
	case "r":
		m.state.Message, m.state.MessageIsError = "새로고침 중…", false
		return m, m.load()
	case "a":
		return m.decide(true)
	case "x":
		return m.decide(false)
	}
	return m, nil
}

func (m Model) selectProject(index int) (tea.Model, tea.Cmd) {
	if len(m.snapshot.Projects) == 0 {
		return m, nil
	}
	if index < 0 || index >= len(m.snapshot.Projects) {
		return m, nil
	}
	m.snapshot.Selected = index
	m.state.Cursor = 0
	return m, m.load()
}

// decide approves or rejects the selected approval.
//
// The authority check happens here, inside the action, rather than once when
// the TUI starts. A screen is not a permission: an implementation session that
// runs `goalforge tui` must be refused exactly as it is refused
// `goalforge approval approve`, or this becomes the way around the boundary
// every other surface enforces.
func (m Model) decide(approve bool) (tea.Model, tea.Cmd) {
	if m.state.Tab != TabApprovals || len(m.snapshot.Approvals) == 0 {
		return m, nil
	}
	if m.state.Cursor >= len(m.snapshot.Approvals) {
		return m, nil
	}
	operation := "승인"
	if !approve {
		operation = "승인 반려"
	}
	if err := policy.RequireOperator(operation); err != nil {
		m.state.Message, m.state.MessageIsError = err.Error(), true
		return m, nil
	}
	approval := m.snapshot.Approvals[m.state.Cursor]
	loader := m.loader
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if approve {
			if err := loader.Approve(ctx, approval.ProjectID, approval.ID); err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{message: "승인했습니다: " + approval.ID}
		}
		// A rejection recorded without a category says nothing about where the
		// automation keeps failing, so the TUI records the one fact it can be
		// sure of and points at the CLI for the rest.
		if err := loader.Reject(ctx, approval.ProjectID, approval.ID, "other", "TUI에서 반려"); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{message: "반려했습니다: " + approval.ID + " (사유 분류는 goalforge approval reject --category 로 기록하세요)"}
	}
}

// rowCount is how many rows the current tab can move through, so the cursor
// cannot leave the list it belongs to.
func (m Model) rowCount() int {
	switch m.state.Tab {
	case TabWork:
		return len(m.snapshot.Work)
	case TabApprovals:
		return len(m.snapshot.Approvals)
	default:
		return 0
	}
}

func clampCursor(cursor, count int) int {
	if count <= 0 || cursor < 0 {
		return 0
	}
	if cursor >= count {
		return count - 1
	}
	return cursor
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	return View(m.snapshot, m.state)
}
