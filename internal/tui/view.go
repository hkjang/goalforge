package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/goalforge/goalforge/internal/app"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// Tabs are the questions a person actually arrives with, in the order they
// usually arrive in: what is happening, is it proven done, what is being
// worked on, what needs me, and what would happen if I ran it now.
const (
	TabOverview = iota
	TabCriteria
	TabWork
	TabApprovals
	TabPlan
	tabCount
)

var tabNames = [tabCount]string{"개요", "완료 조건", "작업", "승인", "계획"}

// Styles are resolved once. Colour is an accent on top of a layout that is
// already readable without it, so a monochrome terminal or NO_COLOR loses
// emphasis and nothing else.
var (
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleDim      = lipgloss.NewStyle().Faint(true)
	styleTabOn    = lipgloss.NewStyle().Bold(true).Underline(true)
	styleTabOff   = lipgloss.NewStyle().Faint(true)
	styleOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleBad      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleSelected = lipgloss.NewStyle().Reverse(true)
)

// pad right-pads to a display width. Go's %-Ns counts runes, and a Korean
// syllable occupies two columns, so format-string padding tears every column
// in this program apart.
func pad(value string, width int) string {
	if lipgloss.Width(value) > width {
		// Truncation cannot always land exactly on the field width — a
		// double-width syllable either fits or it does not — so the clipped
		// value is padded back up. Without this every column after a clipped
		// Korean name shifts by one.
		value = truncate(value, width)
	}
	if fill := width - lipgloss.Width(value); fill > 0 {
		return value + strings.Repeat(" ", fill)
	}
	return value
}

// truncate cuts to a display width, leaving a marker so a clipped value is
// never mistaken for a short one.
func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	for _, r := range value {
		candidate := b.String() + string(r)
		if lipgloss.Width(candidate) > width-1 {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "…"
}

// bar draws a proportion without claiming precision the number does not have.
func bar(percent float64, width int) string {
	if width < 4 {
		return ""
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int(percent / 100 * float64(width))
	return "[" + strings.Repeat("█", filled) + strings.Repeat("·", width-filled) + "]"
}

// criterionMark states a criterion's standing in text as well as colour, so
// the screen is readable in a monochrome terminal and by someone who cannot
// distinguish the colours.
func criterionMark(status string) (string, lipgloss.Style) {
	switch status {
	case "MET":
		return "✓ 충족", styleOK
	case "UNMET":
		return "△ 기준 미달", styleWarn
	case "STALE":
		return "↻ 재검증 필요", styleWarn
	case "WRONG_KIND":
		return "✗ 검증 종류 불일치", styleBad
	default:
		return "○ 증거 없음", styleDim
	}
}

// View is the whole screen. It is a pure function of the snapshot and the
// cursor, which is what makes every state below testable without a terminal.
func View(snapshot Snapshot, state UIState) string {
	if state.Width < 40 || state.Height < 10 {
		return "터미널이 너무 좁습니다 (최소 40x10)"
	}
	if state.ShowHelp {
		return fitWidth(helpView(state), state.Width)
	}
	var sections []string
	sections = append(sections, header(snapshot, state))
	if warning := integrityBanner(snapshot); warning != "" {
		sections = append(sections, warning)
	}
	sections = append(sections, tabBar(state.Tab, state.Width))
	body := ""
	switch state.Tab {
	case TabOverview:
		body = overviewView(snapshot, state)
	case TabCriteria:
		body = criteriaView(snapshot, state)
	case TabWork:
		body = workView(snapshot, state)
	case TabApprovals:
		body = approvalsView(snapshot, state)
	case TabPlan:
		body = planView(snapshot, state)
	}
	sections = append(sections, body)
	sections = append(sections, footer(snapshot, state))
	// Every line is fitted here rather than at each call site. A line wider
	// than the terminal wraps, and one wrapped line pushes everything below it
	// out of place — so the invariant is enforced in one place that a view
	// added later cannot forget.
	return fitWidth(strings.Join(sections, "\n"), state.Width)
}

// integrityBanner sits above everything because a screen whose records were
// altered is not reporting on the project, it is reporting on whoever altered
// them. It is silent when there is nothing to say.
func integrityBanner(snapshot Snapshot) string {
	if len(snapshot.Integrity.Findings) == 0 {
		return ""
	}
	first := snapshot.Integrity.Findings[0]
	return styleBad.Render(fmt.Sprintf("  ⚠ 기록 무결성 %d건 — %s %s: %s  (goalforge integrity verify)",
		len(snapshot.Integrity.Findings), first.RecordKind, first.Kind, first.Detail))
}

func header(snapshot Snapshot, state UIState) string {
	if len(snapshot.Projects) == 0 {
		return styleTitle.Render("GoalForge") + "  " + styleDim.Render("등록된 프로젝트가 없습니다 — goalforge project init")
	}
	row := snapshot.Projects[snapshot.Selected]
	left := styleTitle.Render(truncate(row.Project.Name, 24))
	// Work progress and proven completion are shown side by side because they
	// are different numbers and the gap between them is the thing worth
	// noticing: 100% of the work with an unmet criterion is not done.
	criteria := fmt.Sprintf("조건 %d/%d", row.CriteriaMet, row.CriteriaTotal)
	style := styleWarn
	if row.CriteriaTotal > 0 && row.CriteriaMet == row.CriteriaTotal {
		style = styleOK
	}
	if row.CriteriaTotal == 0 {
		criteria, style = "조건 없음", styleBad
	}
	line := fmt.Sprintf("%s  %s  작업 %s %5.1f%%  %s  %s",
		left, stateChip(row.Project.State), bar(row.Progress, 12), row.Progress,
		style.Render(criteria), styleDim.Render(fmt.Sprintf("%d/%d 프로젝트", snapshot.Selected+1, len(snapshot.Projects))))
	return line
}

func stateChip(state string) string {
	switch state {
	case "COMPLETED":
		return styleOK.Render("완료")
	case "FAILED", "REPAIR_REQUIRED":
		return styleBad.Render(stateLabel(state))
	case "RUNNING", "VERIFYING", "CHECKPOINTING":
		return styleWarn.Render(stateLabel(state))
	default:
		return styleDim.Render(stateLabel(state))
	}
}

func stateLabel(state string) string {
	labels := map[string]string{"READY": "준비됨", "RUNNING": "실행 중", "VERIFYING": "검증 중",
		"CHECKPOINTING": "기록 중", "COMPLETED": "완료", "FAILED": "실패",
		"REPAIR_REQUIRED": "복구 필요", "PAUSED": "일시정지"}
	if label, ok := labels[state]; ok {
		return label
	}
	return state
}

func tabBar(active, width int) string {
	var parts []string
	for i, name := range tabNames {
		label := fmt.Sprintf(" %d %s ", i+1, name)
		if i == active {
			parts = append(parts, styleTabOn.Render(label))
			continue
		}
		parts = append(parts, styleTabOff.Render(label))
	}
	return truncate(strings.Join(parts, " "), width)
}

// bodyHeight is the room left for a tab's content after the chrome.
func (s UIState) bodyHeight() int {
	height := s.Height - 4
	if height < 1 {
		return 1
	}
	return height
}

func overviewView(snapshot Snapshot, state UIState) string {
	var lines []string
	if len(snapshot.Projects) == 0 {
		return styleDim.Render("  goalforge project init --name <이름> --provider <제공자> 로 시작하세요")
	}
	lines = append(lines, styleDim.Render("  프로젝트"))
	start, end, above, below := window(len(snapshot.Projects), snapshot.Selected, projectListHeight(state))
	if above {
		lines = append(lines, moreMarker(start, true))
	}
	for i := start; i < end; i++ {
		row := snapshot.Projects[i]
		marker := "  "
		if i == snapshot.Selected {
			marker = "▸ "
		}
		line := fmt.Sprintf("%s%s %s %s  %s",
			marker, pad(truncate(row.Project.Name, 20), 20), pad(stateLabel(row.Project.State), 8),
			pad(fmt.Sprintf("%5.1f%%", row.Progress), 7), truncate(row.GoalTitle, 30))
		if row.PendingApprovals > 0 {
			line += styleWarn.Render(fmt.Sprintf("  승인 대기 %d", row.PendingApprovals))
		}
		if i == snapshot.Selected {
			line = styleSelected.Render(pad(line, state.Width-1))
		}
		lines = append(lines, line)
	}
	if below {
		lines = append(lines, moreMarker(len(snapshot.Projects)-end, false))
	}
	if snapshot.Goal != nil {
		lines = append(lines, "", styleDim.Render("  목표"), "  "+truncate(snapshot.Goal.Title, state.Width-4))
		if snapshot.Goal.Objective != "" {
			lines = append(lines, styleDim.Render("  "+truncate(snapshot.Goal.Objective, state.Width-4)))
		}
	}
	if len(snapshot.Runs) > 0 {
		lines = append(lines, "", styleDim.Render("  최근 실행"))
		for _, run := range snapshot.Runs {
			if len(lines) >= state.bodyHeight() {
				break
			}
			lines = append(lines, truncate(fmt.Sprintf("  %s %s %s",
				pad(run.ID, 26), pad(run.State, 16), styleDim.Render(relativeTime(run.StartedAt))), state.Width))
		}
	}
	return clip(lines, state.bodyHeight())
}

func criteriaView(snapshot Snapshot, state UIState) string {
	if snapshot.Goal == nil {
		return styleDim.Render("  목표가 없습니다 — goalforge goal set")
	}
	if len(snapshot.Progress.Criteria) == 0 {
		return styleBad.Render("  완료 조건이 없어 이 목표는 완료로 판정될 수 없습니다")
	}
	lines := []string{truncate(styleDim.Render(fmt.Sprintf("  %s %s %s %s",
		pad("조건", 22), pad("기준", 10), pad("측정", 10), "상태")), state.Width)}
	for _, criterion := range snapshot.Progress.Criteria {
		label, style := criterionMark(criterion.Status)
		detail := ""
		switch criterion.Status {
		case "STALE":
			detail = "  " + criterion.StaleReason
		case "WRONG_KIND":
			detail = "  " + criterion.KindMismatch()
		}
		lines = append(lines, truncate(fmt.Sprintf("  %s %s %s %s%s",
			pad(criterion.Type, 22), pad(criterion.ExpectedValue, 10), pad(orDash(criterion.ActualValue), 10),
			style.Render(label), styleDim.Render(detail)), state.Width))
	}
	// Completion is restated here because a criteria screen that lists every
	// row without saying whether they add up to "done" makes the reader do
	// arithmetic the program already did.
	lines = append(lines, "")
	if snapshot.Progress.Complete {
		lines = append(lines, "  "+styleOK.Render("모든 필수 조건이 충족되었고 작업이 모두 끝났습니다"))
	} else {
		lines = append(lines, "  "+styleWarn.Render("완료 아님: "+snapshot.Progress.IncompleteReason))
	}
	return clip(lines, state.bodyHeight())
}

func workView(snapshot Snapshot, state UIState) string {
	if len(snapshot.Work) == 0 {
		return styleDim.Render("  작업이 없습니다 — goalforge work add")
	}
	lines := []string{styleDim.Render(fmt.Sprintf("  %s %s %s",
		pad("상태", 10), pad("제목", 34), "막힌 이유"))}
	start, end, above, below := window(len(snapshot.Work), state.Cursor, state.bodyHeight()-3)
	if above {
		lines = append(lines, moreMarker(start, true))
	}
	for i := start; i < end; i++ {
		row := snapshot.Work[i]
		marker := "  "
		if i == state.Cursor {
			marker = "▸ "
		}
		blocker := styleDim.Render("—")
		if row.Blocked() {
			blocker = styleWarn.Render(truncate(row.Blockers[0].Detail, 40))
		}
		line := fmt.Sprintf("%s%s %s %s", marker,
			pad(workStatusLabel(row.Item.Status), 10), pad(truncate(row.Item.Title, 34), 34), blocker)
		if i == state.Cursor {
			line = styleSelected.Render(pad(truncate(line, state.Width-1), state.Width-1))
		}
		lines = append(lines, line)
	}
	if below {
		lines = append(lines, moreMarker(len(snapshot.Work)-end, false))
	}
	return clip(lines, state.bodyHeight())
}

func workStatusLabel(status string) string {
	labels := map[string]string{"BACKLOG": "대기", "APPROVED": "승인됨", "IN_PROGRESS": "진행 중",
		"VERIFYING": "검증 중", "DONE": "완료", "BLOCKED": "보류", "DISCARDED": "폐기"}
	if label, ok := labels[status]; ok {
		return label
	}
	return status
}

func approvalsView(snapshot Snapshot, state UIState) string {
	if len(snapshot.Approvals) == 0 {
		return styleDim.Render("  대기 중인 승인이 없습니다")
	}
	var lines []string
	// Each selected row also prints its grounds, so a window sized by rows
	// alone would overflow; one line is reserved for that.
	start, end, above, below := window(len(snapshot.Approvals), state.Cursor, state.bodyHeight()-3)
	if above {
		lines = append(lines, moreMarker(start, true))
	}
	for i := start; i < end; i++ {
		approval := snapshot.Approvals[i]
		marker := "  "
		if i == state.Cursor {
			marker = "▸ "
		}
		line := fmt.Sprintf("%s%s  %s", marker, pad(approvalLabel(approval.ActionType), 18),
			truncate(approval.Reason, state.Width-26))
		if i == state.Cursor {
			line = styleSelected.Render(pad(truncate(line, state.Width-1), state.Width-1))
		}
		lines = append(lines, line)
		// The grounds for the decision go under the row being decided: an
		// approval screen that shows only the action type asks for consent
		// without saying to what.
		if i == state.Cursor {
			for _, detail := range approvalGrounds(approval) {
				lines = append(lines, "      "+styleDim.Render(detail))
			}
		}
	}
	if below {
		lines = append(lines, moreMarker(len(snapshot.Approvals)-end, false))
	}
	return clip(lines, state.bodyHeight())
}

func approvalGrounds(approval store.PendingApproval) []string {
	var grounds []string
	if approval.Scope.WorkItemID != "" {
		grounds = append(grounds, "작업 "+approval.Scope.WorkItemID)
	}
	if approval.Scope.CommitSHA != "" {
		grounds = append(grounds, "커밋 "+shortSHA(approval.Scope.CommitSHA))
	}
	if approval.Scope.TargetRef != "" {
		grounds = append(grounds, "대상 "+approval.Scope.TargetRef)
	}
	if approval.Scope.FilesChanged > 0 {
		grounds = append(grounds, fmt.Sprintf("파일 %d개", approval.Scope.FilesChanged))
	}
	grounds = append(grounds, "요청 "+relativeTime(approval.RequestedAt))
	return []string{strings.Join(grounds, " · ")}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func approvalLabel(actionType string) string {
	labels := map[string]string{
		store.ApprovalProtectedFiles: "보호 파일 수정",
		store.ApprovalRemoveTests:    "테스트 삭제",
		store.ApprovalPublishBranch:  "원격 게시",
		store.ApprovalMergeBranch:    "기본 브랜치 병합",
	}
	if label, ok := labels[actionType]; ok {
		return label
	}
	return actionType
}

func planView(snapshot Snapshot, state UIState) string {
	if snapshot.Plan == nil {
		return styleDim.Render("  계획을 만들 수 없습니다 (프로젝트나 목표가 없습니다)")
	}
	var lines []string
	if snapshot.Plan.WorkItem != nil {
		lines = append(lines, "  다음 작업: "+styleTitle.Render(truncate(snapshot.Plan.WorkItem.Title, state.Width-14)))
		if snapshot.Plan.SelectionReason != "" {
			lines = append(lines, styleDim.Render("  "+truncate(snapshot.Plan.SelectionReason, state.Width-4)))
		}
		// The forecast is shown as the range it is. A single expected number
		// reads as a certainty the history does not support.
		forecast := snapshot.Plan.Forecast
		if forecast.Samples > 0 {
			lines = append(lines, styleDim.Render(fmt.Sprintf("  예상 %s~%s 토큰 (중앙값 %s, 표본 %d, 신뢰도 %s)",
				humanTokens(forecast.Low), humanTokens(forecast.High), humanTokens(forecast.Expected),
				forecast.Samples, forecast.Confidence)))
		} else {
			lines = append(lines, styleDim.Render("  실행 기록이 없어 비용을 예측할 수 없습니다"))
		}
	} else {
		lines = append(lines, "  "+styleDim.Render("실행할 수 있는 작업이 없습니다"))
	}
	lines = append(lines, "")
	blocked := false
	// Ordered worst-first: a screen that buries the one blocking finding under
	// a list of green checks reports a problem without communicating it.
	checks := append([]app.PlanCheck(nil), snapshot.Plan.Checks...)
	sort.SliceStable(checks, func(i, j int) bool { return severity(checks[i].Level) < severity(checks[j].Level) })
	for _, check := range checks {
		style := styleDim
		switch check.Level {
		case "BLOCK":
			style, blocked = styleBad, true
		case "WARN":
			style = styleWarn
		case "OK":
			style = styleOK
		}
		lines = append(lines, truncate(fmt.Sprintf("  %s %s %s",
			style.Render(pad(check.Level, 5)), pad(check.Name, 20), check.Detail), state.Width))
	}
	lines = append(lines, "")
	if blocked {
		lines = append(lines, "  "+styleBad.Render("지금 실행하면 거부됩니다"))
	} else {
		lines = append(lines, "  "+styleOK.Render("지금 실행할 수 있습니다"))
	}
	return clip(lines, state.bodyHeight())
}

func severity(level string) int {
	switch level {
	case "BLOCK":
		return 0
	case "WARN":
		return 1
	default:
		return 2
	}
}

func footer(snapshot Snapshot, state UIState) string {
	if state.Message != "" {
		style := styleDim
		if state.MessageIsError {
			style = styleBad
		}
		return "  " + style.Render(state.Message)
	}
	keys := "? 도움말 · 1-5 탭 · ↑↓ 이동 · r 새로고침 · q 종료"
	if state.Tab == TabApprovals && len(snapshot.Approvals) > 0 {
		keys = "a 승인 · x 반려 · " + keys
	}
	age := styleDim.Render(relativeTime(snapshot.TakenAt) + " 기준")
	return "  " + styleDim.Render(keys) + "  " + age
}

func clip(lines []string, height int) string {
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// window returns the slice of a list that keeps the cursor on screen, and
// whether rows are hidden above or below.
//
// Without it the first screenful was rendered and the rest dropped, so a
// cursor past the bottom moved invisibly: pressing down did nothing anyone
// could see, which is indistinguishable from the key not working. The window
// is computed from the cursor rather than stored, so it cannot drift out of
// step with it.
func window(total, cursor, height int) (start, end int, above, below bool) {
	if height < 1 {
		height = 1
	}
	if total <= height {
		return 0, total, false, false
	}
	// Keep the cursor roughly centred once the list is long enough to scroll,
	// so the rows around it stay visible on both sides.
	start = cursor - height/2
	if start < 0 {
		start = 0
	}
	if start > total-height {
		start = total - height
	}
	return start, start + height, start > 0, start+height < total
}

// moreMarker says how many rows are out of sight, because a list that silently
// shows part of itself reads as the whole thing.
func moreMarker(count int, above bool) string {
	if count <= 0 {
		return ""
	}
	if above {
		return styleDim.Render(fmt.Sprintf("  ↑ 위로 %d개 더", count))
	}
	return styleDim.Render(fmt.Sprintf("  ↓ 아래로 %d개 더", count))
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// relativeTime says how old something is, because an absolute timestamp makes
// the reader do the subtraction that decides whether it matters.
func relativeTime(at time.Time) string {
	if at.IsZero() {
		return "시각 없음"
	}
	elapsed := time.Since(at)
	switch {
	case elapsed < 0:
		return at.Local().Format("01-02 15:04")
	case elapsed < time.Minute:
		return "방금"
	case elapsed < time.Hour:
		return fmt.Sprintf("%d분 전", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%d시간 전", int(elapsed.Hours()))
	default:
		return at.Local().Format("01-02 15:04")
	}
}

// humanTokens keeps large counts readable without pretending to a precision
// the forecast does not have.
func humanTokens(count int64) string {
	switch {
	case count >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(count)/1_000_000)
	case count >= 1_000:
		return fmt.Sprintf("%.0fk", float64(count)/1_000)
	default:
		return fmt.Sprintf("%d", count)
	}
}

// fitWidth clips every line to the terminal width.
func fitWidth(rendered string, width int) string {
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = truncate(line, width)
		}
	}
	return strings.Join(lines, "\n")
}

// projectListHeight leaves room on the overview for the goal and the recent
// runs below the list, so a long project list does not push them off screen.
func projectListHeight(state UIState) int {
	height := state.bodyHeight() - 8
	if height < 3 {
		return 3
	}
	if height > 12 {
		return 12
	}
	return height
}

// helpView lists every key on its own screen.
//
// The footer is one line and gets truncated on a narrow terminal, which is
// where someone is most likely to be looking for it. A key that exists and
// cannot be discovered is a key that does not exist.
func helpView(state UIState) string {
	rows := [][2]string{
		{"1 – 5", "탭 이동 (개요 · 완료 조건 · 작업 · 승인 · 계획)"},
		{"Tab / Shift+Tab", "다음 / 이전 탭"},
		{"↑ ↓  또는  k j", "목록 이동 (개요에서는 프로젝트 선택)"},
		{"← →  또는  h l", "프로젝트 전환"},
		{"PgUp / PgDn", "한 화면씩"},
		{"Home / End  (g / G)", "처음 / 끝"},
		{"a", "선택한 승인을 승인 (승인 탭)"},
		{"x", "선택한 승인을 반려 (승인 탭)"},
		{"r", "새로고침"},
		{"?", "이 화면 닫기"},
		{"q / Esc / Ctrl+C", "종료"},
	}
	lines := []string{styleTitle.Render("  GoalForge 터미널 — 키"), ""}
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("  %s  %s", pad(row[0], 20), styleDim.Render(row[1])))
	}
	lines = append(lines, "",
		styleDim.Render("  승인 결정은 누를 때 권한을 확인합니다. 구현 세션은 이 화면을 열어도 승인할 수 없습니다."))
	return clip(lines, state.Height-1)
}
