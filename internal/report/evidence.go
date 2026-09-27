// Package report renders assembled records into documents a person reads:
// an evidence bundle for handover, release review, or audit.
package report

import (
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// EvidenceHTML writes a self-contained page. It embeds its own styles and
// loads nothing external, because the document has to keep working when it is
// mailed, archived, or opened years later on a machine with no network.
func EvidenceHTML(w io.Writer, bundle store.EvidenceBundle) error {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="ko"><head><meta charset="utf-8">`)
	b.WriteString(`<title>` + esc(bundle.Project.Name) + ` 증거 묶음</title><style>`)
	b.WriteString(`body{font:15px/1.6 system-ui,sans-serif;max-width:1000px;margin:40px auto;padding:0 20px;color:#1a1c22}` +
		`h1{font-size:26px;margin-bottom:4px}h2{font-size:18px;margin-top:34px;border-bottom:1px solid #e3e6ee;padding-bottom:6px}` +
		`h3{font-size:15px;margin:18px 0 6px}table{width:100%;border-collapse:collapse;font-size:13.5px;margin:8px 0}` +
		`th{text-align:left;color:#5a6172;font-weight:600;border-bottom:1px solid #e3e6ee;padding:6px 8px 6px 0}` +
		`td{padding:6px 8px 6px 0;border-bottom:1px solid #f0f2f7;vertical-align:top}` +
		`.sub{color:#5a6172}.mono{font-family:ui-monospace,monospace;font-size:12.5px}` +
		`.tag{display:inline-block;font-size:11.5px;padding:2px 8px;border-radius:999px;border:1px solid #d5d9e4;color:#5a6172}` +
		`.met{color:#15803d;border-color:#bbf7d0;background:#f0fdf4}.unmet{color:#b91c1c;border-color:#fecaca;background:#fef2f2}` +
		`.stale{color:#a16207;border-color:#fde68a;background:#fffbeb}` +
		`.note{background:#f8f9fc;border:1px solid #e3e6ee;border-radius:8px;padding:10px 12px;margin:8px 0}` +
		`</style></head><body>`)
	fmt.Fprintf(&b, `<h1>%s</h1><div class="sub">%s — 생성 %s</div>`,
		esc(bundle.Project.Name), esc(bundle.Goal.Title), bundle.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, `<div class="note">진행률 %.1f%% · 완료 조건 충족 %t · 실행 %d회 · 비용 $%.4f<br>`+
		`저장소 <span class="mono">%s</span> · 기본 브랜치 <span class="mono">%s</span> · 제공자 %s %s</div>`,
		bundle.Progress.Percent, bundle.Progress.Complete, bundle.Cost.RunsTotal, bundle.Cost.CostUSD,
		esc(bundle.Project.RepositoryPath), esc(bundle.Project.DefaultBranch), esc(bundle.Project.Provider), esc(bundle.Project.Model))

	b.WriteString(`<h2>완료 조건과 근거</h2><table><tr><th>조건</th><th>기준</th><th>측정값</th><th>판정</th><th>근거</th></tr>`)
	for _, criterion := range bundle.Criteria {
		fmt.Fprintf(&b, `<tr><td>%s</td><td class="mono">%s</td><td class="mono">%s</td><td>%s</td><td class="sub mono">%s</td></tr>`,
			esc(criterion.Type), esc(dash(criterion.ExpectedValue)), esc(dash(criterion.ActualValue)),
			criterionTag(criterion), esc(evidenceRef(criterion)))
	}
	b.WriteString(`</table>`)
	if bundle.Integration.Pending {
		fmt.Fprintf(&b, `<div class="note">통합 검증 미완료: %s</div>`, esc(bundle.Integration.Reason))
	} else if bundle.Integration.LastSHA != "" {
		fmt.Fprintf(&b, `<div class="note">통합 검증 통과 — 커밋 <span class="mono">%s</span></div>`, esc(short(bundle.Integration.LastSHA)))
	}

	b.WriteString(`<h2>목표 이력</h2><table><tr><th>버전</th><th>제목</th><th>변경 사유</th><th>완료 조건</th><th>시각</th></tr>`)
	for _, version := range bundle.GoalHistory {
		var criteria []string
		for _, criterion := range version.Criteria {
			criteria = append(criteria, criterion.Type+"="+criterion.ExpectedValue)
		}
		fmt.Fprintf(&b, `<tr><td>v%d</td><td>%s</td><td class="sub">%s</td><td class="mono">%s</td><td class="sub">%s</td></tr>`,
			version.Version, esc(version.Title), esc(dash(version.ChangeReason)), esc(strings.Join(criteria, ", ")),
			version.CreatedAt.Format("2006-01-02 15:04"))
	}
	b.WriteString(`</table>`)

	b.WriteString(`<h2>작업과 검증</h2>`)
	for _, entry := range bundle.WorkItems {
		fmt.Fprintf(&b, `<h3>%s <span class="tag">%s</span></h3>`, esc(entry.Item.Title), esc(entry.Item.Status))
		fmt.Fprintf(&b, `<div class="sub mono">%s</div>`, esc(entry.Item.ID))
		if entry.Item.Objective != "" {
			fmt.Fprintf(&b, `<div class="sub">목적: %s</div>`, esc(entry.Item.Objective))
		}
		if entry.Item.Acceptance != "" {
			fmt.Fprintf(&b, `<div class="sub">완료 기준: %s</div>`, esc(entry.Item.Acceptance))
		}
		fmt.Fprintf(&b, `<div class="sub">실행 %d회 · %d 토큰 · $%.4f`, entry.Runs, entry.Tokens, entry.CostUSD)
		if entry.Commit != nil {
			fmt.Fprintf(&b, ` · 커밋 <span class="mono">%s</span> (%s, 파일 %d개)`,
				esc(short(entry.Commit.CommitSHA)), esc(entry.Commit.Branch), entry.Commit.FilesCommitted)
		}
		b.WriteString(`</div>`)
		if len(entry.Verifications) == 0 {
			continue
		}
		b.WriteString(`<table><tr><th>게이트</th><th>결과</th><th>측정값</th><th>종료 코드</th><th>명령</th></tr>`)
		for _, result := range entry.Verifications {
			fmt.Fprintf(&b, `<tr><td>%s%s</td><td>%s</td><td class="mono">%s</td><td>%d</td><td class="sub mono">%s</td></tr>`,
				esc(result.CheckType), optional(result.Required), statusTag(result.Status), esc(dash(result.ActualValue)),
				result.ExitCode, esc(result.Command))
		}
		b.WriteString(`</table>`)
	}

	if len(bundle.Decisions) > 0 {
		b.WriteString(`<h2>설계 결정</h2><table><tr><th>제목</th><th>결정</th><th>제외한 대안</th><th>상태</th><th>기준 커밋</th></tr>`)
		for _, decision := range bundle.Decisions {
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td class="sub">%s</td><td><span class="tag">%s</span></td><td class="mono sub">%s</td></tr>`,
				esc(decision.Title), esc(decision.Decision), esc(dash(decision.Alternatives)), esc(decision.Status), esc(short(decision.BaseCommit)))
		}
		b.WriteString(`</table>`)
	}

	b.WriteString(`<h2>승인 이력</h2>`)
	if len(bundle.Approvals) == 0 {
		b.WriteString(`<div class="sub">기록된 승인 요청이 없습니다.</div>`)
	} else {
		b.WriteString(`<table><tr><th>행위</th><th>대상</th><th>사유</th><th>상태</th><th>반려 사유</th><th>요청</th></tr>`)
		for _, approval := range bundle.Approvals {
			target := "-"
			if approval.Scope.CommitSHA != "" {
				target = approval.Scope.WorkItemID + " @ " + short(approval.Scope.CommitSHA) + " → " + approval.Scope.TargetRef
			}
			rejection := dash(approval.RejectionCategory)
			if approval.RejectionNote != "" {
				rejection += " — " + approval.RejectionNote
			}
			fmt.Fprintf(&b, `<tr><td>%s</td><td class="mono">%s</td><td class="sub">%s</td><td><span class="tag">%s</span></td><td class="sub">%s</td><td class="sub">%s</td></tr>`,
				esc(approval.ActionType), esc(target), esc(approval.Reason), esc(approval.Status), esc(rejection),
				approval.RequestedAt.Format("2006-01-02 15:04"))
		}
		b.WriteString(`</table>`)
	}

	// Relaxations belong in the record precisely because they are the thing a
	// reader would otherwise have to take on trust.
	if len(bundle.Relaxations) > 0 {
		b.WriteString(`<h2>검증 기준 변경</h2><div class="sub">통과를 쉽게 만든 변경입니다. 코드가 나아진 것과 구분해서 읽어야 합니다.</div>`)
		b.WriteString(`<table><tr><th>유형</th><th>내용</th><th>이전</th><th>이후</th><th>시각</th></tr>`)
		for _, relaxation := range bundle.Relaxations {
			fmt.Fprintf(&b, `<tr><td><span class="tag unmet">%s</span></td><td>%s</td><td class="mono">%s</td><td class="mono">%s</td><td class="sub">%s</td></tr>`,
				esc(relaxation.Kind), esc(relaxation.Detail), esc(dash(relaxation.Before)), esc(dash(relaxation.After)),
				relaxation.CreatedAt.Format("2006-01-02 15:04"))
		}
		b.WriteString(`</table>`)
	}
	b.WriteString(`</body></html>`)
	_, err := io.WriteString(w, b.String())
	return err
}

func criterionTag(status store.CriterionStatus) string {
	switch status.Status {
	case "MET":
		return `<span class="tag met">충족</span>`
	case "UNMET":
		return `<span class="tag unmet">기준 미달</span>`
	case "STALE":
		return `<span class="tag stale">재검증 필요</span>`
	default:
		return `<span class="tag">증거 없음</span>`
	}
}

func statusTag(status string) string {
	if status == "PASSED" {
		return `<span class="tag met">PASSED</span>`
	}
	return `<span class="tag unmet">` + esc(status) + `</span>`
}

func evidenceRef(status store.CriterionStatus) string {
	switch status.Status {
	case "STALE":
		return status.StaleReason
	case "NO_EVIDENCE":
		return "없음"
	default:
		ref := status.RunID
		if ref == "" {
			ref = "통합/수동 검증"
		}
		if !status.MeasuredAt.IsZero() {
			ref += " " + status.MeasuredAt.Format("2006-01-02 15:04")
		}
		return ref
	}
}

func optional(required bool) string {
	if required {
		return ""
	}
	return ` <span class="tag">선택</span>`
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func esc(value string) string { return html.EscapeString(value) }
