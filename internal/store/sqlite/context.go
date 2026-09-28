package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

// ContextItem is one piece of assembled context with its provenance. A claim
// an execution session is asked to rely on has to say where it came from and
// as of when, or the session cannot tell stale context from current fact.
type ContextItem struct {
	Kind, Title, Body string
	// Source names where this came from (a decision ID, a run ID, a gate).
	Source string
	// AsOf is when the underlying fact was recorded.
	AsOf time.Time
	// Standing separates what is measured from what was decided from what is
	// merely assumed. Handed a flat list, a session reads a two-month-old
	// architecture note with the same authority as a gate that passed a minute
	// ago, and acts on it.
	Standing string
	// Caveat is why this may no longer hold. Present only when it does not.
	Caveat string
}

// Context standings. Measured is backed by evidence GoalForge recorded;
// Decided is true because someone chose it, which can be superseded but not
// disproved; Assumed is stated without either.
const (
	ContextMeasured = "측정된 사실"
	ContextDecided  = "결정"
	ContextAssumed  = "확인되지 않음"
	ContextPolicy   = "규칙"
)

// ContextPackage is everything a session needs before touching a work item:
// what it is for, what must not change, what has already been tried and
// failed, and how the result will be judged. Assembling it once and passing it
// in is what stops every new session re-deriving the same conclusions and
// repeating the same mistakes.
type ContextPackage struct {
	WorkItemID   string
	Decisions    []ContextItem
	Constraints  []ContextItem
	PastFailures []ContextItem
	Verification []ContextItem
}

// Empty reports whether nothing useful was assembled, so callers can leave the
// prompt unchanged rather than adding an empty heading.
func (p ContextPackage) Empty() bool {
	return len(p.Decisions) == 0 && len(p.Constraints) == 0 && len(p.PastFailures) == 0 && len(p.Verification) == 0
}

// BuildContextPackage assembles the context for one work item.
func (s *Store) BuildContextPackage(ctx context.Context, project model.Project, goal model.Goal, work model.WorkItem) (ContextPackage, error) {
	pkg := ContextPackage{WorkItemID: work.ID}
	decisions, err := s.ListDecisions(ctx, project.ID, false)
	if err != nil {
		return pkg, err
	}
	// Each decision is checked against the code it was made about. A decision
	// whose files have moved since is still included — it is the best record
	// of why things are as they are — but it is handed over marked, so the
	// session treats it as something to confirm rather than as fact.
	standings := DecisionStandings(ctx, project.RepositoryPath, decisions)
	standingByID := make(map[string]DecisionStanding, len(standings))
	for _, standing := range standings {
		standingByID[standing.Decision.ID] = standing
	}
	for _, decision := range decisions {
		body := decision.Decision
		if decision.Alternatives != "" {
			body += "\n제외한 대안: " + decision.Alternatives
		}
		if decision.Consequences != "" {
			body += "\n영향: " + decision.Consequences
		}
		source := decision.ID
		if decision.BaseCommit != "" {
			source += " @ " + shortSHA(decision.BaseCommit)
		}
		item := ContextItem{Kind: "decision", Title: decision.Title, Body: body, Source: source,
			AsOf: decision.CreatedAt, Standing: ContextDecided}
		if standing, ok := standingByID[decision.ID]; ok && standing.NeedsReview() {
			item.Standing, item.Caveat = ContextAssumed, standing.Detail
		}
		pkg.Decisions = append(pkg.Decisions, item)
	}
	if work.ChangeScope != "" {
		pkg.Constraints = append(pkg.Constraints, ContextItem{Kind: "scope", Title: "허용된 변경 범위",
			Body: work.ChangeScope + " 밖의 파일을 수정하면 범위 이탈로 검증이 차단됩니다", Source: work.ID, Standing: ContextPolicy})
	}
	pkg.Constraints = append(pkg.Constraints, ContextItem{Kind: "protected", Title: "변경 금지",
		Body: "보호 대상 파일(.env, 키, 인증 설정)은 승인 없이 수정할 수 없습니다. 기본 브랜치에 직접 커밋하지 않습니다.", Source: "policy", Standing: ContextPolicy})
	if work.Acceptance != "" {
		pkg.Constraints = append(pkg.Constraints, ContextItem{Kind: "acceptance", Title: "완료 기준", Body: work.Acceptance, Source: work.ID, Standing: ContextPolicy})
	}
	failures, err := s.recentFailures(ctx, project.ID, work.ID)
	if err != nil {
		return pkg, err
	}
	pkg.PastFailures = failures
	gates, err := s.ListGates(ctx, project.ID)
	if err != nil {
		return pkg, err
	}
	for _, gate := range gates {
		body := strings.Join(gate.Command, " ")
		if gate.ValuePattern != "" {
			body += fmt.Sprintf("  (측정값 %s 이상, 패턴 %s)", gate.SuccessValue, gate.ValuePattern)
		}
		if !gate.Required {
			body += "  (선택 게이트)"
		}
		pkg.Verification = append(pkg.Verification, ContextItem{Kind: "gate", Title: gate.Type, Body: body,
			Source: "gate:" + gate.Type, Standing: ContextPolicy})
	}
	for _, criterion := range goal.Criteria {
		status, statusErr := s.criterionStatus(ctx, goal.ID, criterion)
		if statusErr != nil {
			return pkg, statusErr
		}
		body := fmt.Sprintf("기준 %s, 현재 %s", criterion.ExpectedValue, statusLabel(status))
		// A criterion with no evidence, or with evidence that has gone stale,
		// is not a measured fact however confidently the row reads.
		itemStanding := ContextMeasured
		caveat := ""
		switch status.Status {
		case "NO_EVIDENCE":
			itemStanding, caveat = ContextAssumed, "아직 한 번도 측정되지 않았습니다"
		case "STALE":
			itemStanding, caveat = ContextAssumed, status.StaleReason
		case "WRONG_KIND":
			itemStanding, caveat = ContextAssumed, status.KindMismatch()
		}
		pkg.Verification = append(pkg.Verification, ContextItem{Kind: "criterion", Title: criterion.Type,
			Body: body, Source: status.RunID, AsOf: status.MeasuredAt, Standing: itemStanding, Caveat: caveat})
	}
	return pkg, nil
}

func statusLabel(status CriterionStatus) string {
	switch status.Status {
	case "MET":
		return "충족 (" + status.ActualValue + ")"
	case "UNMET":
		return "기준 미달 (" + status.ActualValue + ")"
	case "WRONG_KIND":
		return "검증 종류 불일치 (" + status.KindMismatch() + ")"
	default:
		return "증거 없음"
	}
}

// recentFailures collects what previous attempts at this work item actually
// failed on, so the next session does not repeat a fix that has already been
// shown not to work.
func (s *Store) recentFailures(ctx context.Context, projectID, workItemID string) ([]ContextItem, error) {
	if workItemID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT v.run_id,v.check_type,v.status,COALESCE(v.failure_kind,''),v.output,v.created_at
FROM verification_results v JOIN runs r ON r.id=v.run_id
WHERE r.project_id=? AND r.work_item_id=? AND v.status<>'PASSED' AND v.required=1
ORDER BY v.id DESC LIMIT 5`, projectID, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ContextItem
	for rows.Next() {
		var runID, checkType, status, kind, output, created string
		if err = rows.Scan(&runID, &checkType, &status, &kind, &output, &created); err != nil {
			return nil, err
		}
		at, _ := time.Parse(time.RFC3339Nano, created)
		result = append(result, ContextItem{Kind: "failure", Title: checkType + " " + status,
			Body: firstLines(output, 6), Source: runID + " (" + kind + ")", AsOf: at, Standing: ContextMeasured})
	}
	return result, rows.Err()
}

// firstLines trims output to the part that identifies a failure; a whole log
// in a prompt costs tokens without adding information.
func firstLines(text string, limit int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > limit {
		lines = append(lines[:limit], "…")
	}
	return strings.Join(lines, "\n")
}
