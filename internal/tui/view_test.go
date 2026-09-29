package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/goalforge/goalforge/internal/app"
	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/muesli/termenv"
)

func TestMain(m *testing.M) {
	// Render without colour so the tests compare what the screen says rather
	// than which escape codes it happened to emit.
	lipgloss.SetColorProfile(termenv.Ascii)
	m.Run()
}

func testState() UIState { return UIState{Width: 120, Height: 30} }

func sampleSnapshot() Snapshot {
	return Snapshot{
		TakenAt: time.Now(),
		Projects: []ProjectRow{{
			Project:  model.Project{ID: "PRJ-1", Name: "결제 서비스", State: "RUNNING"},
			Progress: 100, CriteriaMet: 1, CriteriaTotal: 2, GoalTitle: "결제 실패율 개선",
		}},
		Goal: &model.Goal{ID: "GOAL-1", Title: "결제 실패율 개선", Objective: "재시도 로직을 고친다"},
	}
}

// Korean syllables occupy two terminal columns. Go's %-Ns counts runes, so
// format-string padding tears every column in this program apart; the padding
// here is display-width aware and the test pins that.
func TestPaddingIsDisplayWidthAware(t *testing.T) {
	for _, tc := range []struct{ value string }{{"결제"}, {"abc"}, {"결제a"}, {""}} {
		if got := lipgloss.Width(pad(tc.value, 10)); got != 10 {
			t.Errorf("pad(%q,10) rendered %d columns, want 10", tc.value, got)
		}
	}
	// A value already wider than the field is cut, not allowed to push the
	// columns after it out of alignment.
	if got := lipgloss.Width(pad("아주아주아주긴이름입니다", 8)); got != 8 {
		t.Errorf("an over-wide value must be clipped to the field: %d", got)
	}
}

func TestTruncateMarksWhatItCut(t *testing.T) {
	if got := truncate("결제 실패율 개선", 6); !strings.HasSuffix(got, "…") {
		t.Errorf("a clipped value must say it was clipped: %q", got)
	}
	if got := truncate("short", 20); got != "short" {
		t.Errorf("a value that fits must be untouched: %q", got)
	}
	if lipgloss.Width(truncate("결제 실패율 개선", 7)) > 7 {
		t.Error("truncation must respect display width, not rune count")
	}
}

// The number people get wrong: work can be 100% while the goal is not done,
// because a required criterion is unmet. The header has to show both.
func TestHeaderShowsProvenCompletionBesideWorkProgress(t *testing.T) {
	rendered := View(sampleSnapshot(), testState())
	if !strings.Contains(rendered, "100.0%") {
		t.Errorf("work progress missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "조건 1/2") {
		t.Errorf("proven completion missing, so 100%% reads as done:\n%s", rendered)
	}
}

// Every criterion state has to be legible as text, not only as colour: the
// screen must work in a monochrome terminal and for someone who cannot tell
// the colours apart.
func TestEveryCriterionStateRendersAsText(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.Progress = store.ProgressDetail{Criteria: []store.CriterionStatus{
		{Type: "build_passed", ExpectedValue: "true", ActualValue: "true", Status: "MET", Satisfied: true},
		{Type: "coverage", ExpectedValue: "85", ActualValue: "71.4", Status: "UNMET"},
		{Type: "vet_clean", ExpectedValue: "true", Status: "STALE", StaleReason: "코드가 바뀌었습니다"},
		{Type: "note_saves", ExpectedValue: "true", Status: "WRONG_KIND", RequiredKind: "journey", EvidenceKind: "build"},
		{Type: "latency", ExpectedValue: "200", Status: "NO_EVIDENCE"},
	}, IncompleteReason: "완료 조건 coverage 미충족 (UNMET)"}
	state := testState()
	state.Tab = TabCriteria
	rendered := View(snapshot, state)
	for _, want := range []string{"충족", "기준 미달", "재검증 필요", "검증 종류 불일치", "증거 없음"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("criterion state %q missing:\n%s", want, rendered)
		}
	}
	// The mismatch has to say which kind was needed and which answered, or the
	// user goes and edits code that may be fine.
	if !strings.Contains(rendered, "journey") || !strings.Contains(rendered, "build") {
		t.Errorf("the kind mismatch must name both kinds:\n%s", rendered)
	}
	if !strings.Contains(rendered, "완료 아님") {
		t.Errorf("the screen must state the conclusion, not leave the reader to add it up:\n%s", rendered)
	}
	// The reason comes from the store's single judgment rather than being
	// re-derived here, so every surface gives the same answer.
	if !strings.Contains(rendered, snapshot.Progress.IncompleteReason) {
		t.Errorf("the screen must show the shared reason %q:\n%s", snapshot.Progress.IncompleteReason, rendered)
	}
}

// A goal with no criteria can never complete. That is the silent
// misconfiguration, so the screen says it rather than showing an empty table.
func TestNoCriteriaIsCalledOut(t *testing.T) {
	snapshot := sampleSnapshot()
	state := testState()
	state.Tab = TabCriteria
	if rendered := View(snapshot, state); !strings.Contains(rendered, "완료로 판정될 수 없습니다") {
		t.Errorf("an unjudgeable goal must be called out:\n%s", rendered)
	}
}

// "BACKLOG" on its own never explains why nothing is happening.
func TestWorkRowsShowWhyTheyAreStuck(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.Work = []WorkRow{
		{Item: model.WorkItem{ID: "W1", Title: "세션 저장소 구현", Status: "BACKLOG"},
			Blockers: []store.WorkItemBlocker{{Kind: "DEPENDENCY", Detail: "W0 이(가) 끝나지 않았습니다"}}},
		{Item: model.WorkItem{ID: "W2", Title: "문서 정리", Status: "DONE"}},
	}
	state := testState()
	state.Tab = TabWork
	rendered := View(snapshot, state)
	if !strings.Contains(rendered, "W0 이(가) 끝나지 않았습니다") {
		t.Errorf("the blocker must be shown next to the item:\n%s", rendered)
	}
	if !strings.Contains(rendered, "대기") || !strings.Contains(rendered, "완료") {
		t.Errorf("statuses must be readable:\n%s", rendered)
	}
}

// Approving without seeing what is being approved is consent to nothing in
// particular, so the selected row carries its grounds.
func TestSelectedApprovalShowsItsGrounds(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.Approvals = []store.PendingApproval{{
		Approval: store.Approval{ID: "APR-1", ProjectID: "PRJ-1", ActionType: store.ApprovalMergeBranch,
			Reason: "세션 저장소 병합", RequestedAt: time.Now().Add(-90 * time.Minute),
			Scope: store.ApprovalScope{WorkItemID: "W1", CommitSHA: "abcdef1234567890", TargetRef: "main", FilesChanged: 7}},
		ProjectName: "결제 서비스",
	}}
	state := testState()
	state.Tab = TabApprovals
	rendered := View(snapshot, state)
	for _, want := range []string{"기본 브랜치 병합", "W1", "abcdef123456", "main", "파일 7개", "1시간 전"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the grounds must include %q:\n%s", want, rendered)
		}
	}
	if !strings.Contains(rendered, "a 승인") || !strings.Contains(rendered, "x 반려") {
		t.Errorf("the available decisions must be visible:\n%s", rendered)
	}
}

// A blocking finding buried under a list of green checks is reported but not
// communicated, so the plan is ordered worst-first and states the conclusion.
func TestPlanPutsBlockingFindingsFirst(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.Plan = &app.Plan{Checks: []app.PlanCheck{
		{Level: "OK", Name: "budget", Detail: "예산 충분"},
		{Level: "WARN", Name: "readiness", Detail: "증거가 오래되었습니다"},
		{Level: "BLOCK", Name: "gates", Detail: "검증 게이트가 없습니다"},
	}}
	state := testState()
	state.Tab = TabPlan
	rendered := View(snapshot, state)
	blockAt, okAt := strings.Index(rendered, "BLOCK"), strings.Index(rendered, "OK")
	if blockAt < 0 || okAt < 0 || blockAt > okAt {
		t.Errorf("the blocking finding must come first:\n%s", rendered)
	}
	if !strings.Contains(rendered, "지금 실행하면 거부됩니다") {
		t.Errorf("the plan must state its conclusion:\n%s", rendered)
	}
}

// A terminal too small to lay out is told so, rather than shown a torn screen.
func TestTinyTerminalIsToldRatherThanTorn(t *testing.T) {
	if rendered := View(sampleSnapshot(), UIState{Width: 20, Height: 5}); !strings.Contains(rendered, "너무 좁습니다") {
		t.Errorf("rendered=%q", rendered)
	}
}

// Nothing rendered may exceed the terminal width, or every line wraps and the
// layout collapses.
func TestNoLineExceedsTheTerminalWidth(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.Progress = store.ProgressDetail{Criteria: []store.CriterionStatus{
		{Type: "아주아주아주긴조건이름입니다정말로", ExpectedValue: "true", Status: "WRONG_KIND",
			RequiredKind: "journey", EvidenceKind: "build"}}}
	snapshot.Work = []WorkRow{{Item: model.WorkItem{Title: strings.Repeat("긴제목", 40), Status: "BACKLOG"},
		Blockers: []store.WorkItemBlocker{{Detail: strings.Repeat("사유", 60)}}}}
	snapshot.Approvals = []store.PendingApproval{{Approval: store.Approval{
		ID: "APR-1", ActionType: store.ApprovalMergeBranch, Reason: strings.Repeat("아주 긴 사유 ", 30)}}}
	for width := 40; width <= 200; width += 37 {
		state := UIState{Width: width, Height: 30}
		for tab := 0; tab < tabCount; tab++ {
			state.Tab = tab
			for _, line := range strings.Split(View(snapshot, state), "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("tab %d at width %d produced a %d-column line: %q", tab, width, got, line)
				}
			}
		}
	}
}

// An empty install must explain what to do, not show a blank screen.
func TestEmptyStateGivesTheNextStep(t *testing.T) {
	rendered := View(Snapshot{TakenAt: time.Now()}, testState())
	if !strings.Contains(rendered, "project init") {
		t.Errorf("an empty install must name the first command:\n%s", rendered)
	}
}

// A screen whose records were altered is not reporting on the project, so the
// warning sits above everything rather than behind a tab.
func TestTamperedRecordsWarnAboveEverything(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.Integrity = store.IntegrityReport{Findings: []store.IntegrityFinding{
		{Kind: store.IntegrityRecordEdited, RecordKind: store.ChainEvidence, RecordID: "1",
			Detail: "build_passed 증거가 기록된 뒤 수정되었습니다"}}}
	state := testState()
	for tab := 0; tab < tabCount; tab++ {
		state.Tab = tab
		rendered := View(snapshot, state)
		if !strings.Contains(rendered, "기록 무결성") {
			t.Fatalf("tab %d hid the integrity warning:\n%s", tab, rendered)
		}
		if !strings.Contains(rendered, "integrity verify") {
			t.Fatalf("tab %d did not say how to look into it:\n%s", tab, rendered)
		}
	}
	// A clean install must not carry a scary empty banner.
	clean := sampleSnapshot()
	if strings.Contains(View(clean, testState()), "기록 무결성") {
		t.Error("an intact history must say nothing")
	}
}

// The second reason down looked broken: the first screenful was rendered and
// the rest dropped, so a cursor past the bottom moved invisibly — which is
// indistinguishable from the key not working.
func TestTheCursorStaysOnScreenInALongList(t *testing.T) {
	snapshot := sampleSnapshot()
	for i := 0; i < 80; i++ {
		snapshot.Work = append(snapshot.Work, WorkRow{
			Item: model.WorkItem{ID: fmt.Sprintf("W%02d", i), Title: fmt.Sprintf("작업 %02d", i), Status: "BACKLOG"}})
	}
	state := testState()
	state.Tab = TabWork
	for _, cursor := range []int{0, 1, 40, 78, 79} {
		state.Cursor = cursor
		rendered := View(snapshot, state)
		want := fmt.Sprintf("작업 %02d", cursor)
		if !strings.Contains(rendered, want) {
			t.Fatalf("cursor %d is off screen; %q is not rendered:\n%s", cursor, want, rendered)
		}
	}
}

// A list that silently shows part of itself reads as the whole thing, so what
// is out of sight is counted.
func TestHiddenRowsAreCounted(t *testing.T) {
	snapshot := sampleSnapshot()
	for i := 0; i < 80; i++ {
		snapshot.Work = append(snapshot.Work, WorkRow{Item: model.WorkItem{Title: "x", Status: "BACKLOG"}})
	}
	state := testState()
	state.Tab, state.Cursor = TabWork, 40
	rendered := View(snapshot, state)
	if !strings.Contains(rendered, "위로") || !strings.Contains(rendered, "아래로") {
		t.Fatalf("rows out of sight must be counted:\n%s", rendered)
	}
}

func TestWindowKeepsTheCursorVisible(t *testing.T) {
	for _, tc := range []struct{ total, cursor, height int }{
		{100, 0, 10}, {100, 50, 10}, {100, 99, 10}, {5, 4, 10}, {1, 0, 1},
	} {
		start, end, _, _ := window(tc.total, tc.cursor, tc.height)
		if tc.cursor < start || tc.cursor >= end {
			t.Errorf("total=%d cursor=%d height=%d gave [%d,%d): the cursor is outside it",
				tc.total, tc.cursor, tc.height, start, end)
		}
		if end > tc.total || start < 0 {
			t.Errorf("window [%d,%d) is out of bounds for %d rows", start, end, tc.total)
		}
	}
}
