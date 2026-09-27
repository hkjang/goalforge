package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// TraceablePR is a pull request description that carries what the change was
// for and how it was proven, rather than a list of commits. A reviewer should
// be able to see the goal, the work item, the completion criteria it moves,
// and the evidence without leaving the page.
type TraceablePR struct {
	Title, Body string
	Branch      string
	CommitSHA   string
}

// BuildPRDescription assembles the description for a verified work item.
func (s *Store) BuildPRDescription(ctx context.Context, projectID, workItemID string) (TraceablePR, error) {
	var pr TraceablePR
	goal, err := s.CurrentGoal(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		goal, err = s.LatestGoal(ctx, projectID)
	}
	if err != nil {
		return pr, err
	}
	work, err := s.WorkItemByID(ctx, goal.ID, workItemID)
	if err != nil {
		return pr, err
	}
	commit, err := s.LatestRunCommitForWork(ctx, projectID, workItemID)
	if errors.Is(err, ErrNotFound) {
		return pr, fmt.Errorf("work item %s has no verified commit; only verified work has something to describe", workItemID)
	}
	if err != nil {
		return pr, err
	}
	pr.Title, pr.Branch, pr.CommitSHA = work.Title, commit.Branch, commit.CommitSHA
	results, err := s.VerificationsForRun(ctx, commit.RunID)
	if err != nil {
		return pr, err
	}
	criteria, err := s.CriteriaStatus(ctx, goal)
	if err != nil {
		return pr, err
	}
	changes, err := s.ListRunFileChanges(ctx, commit.RunID)
	if err != nil {
		return pr, err
	}
	var body strings.Builder
	fmt.Fprintf(&body, "## 목표\n\n%s (v%d)\n\n%s\n\n", goal.Title, goal.Version, goal.Objective)
	fmt.Fprintf(&body, "## 작업\n\n- %s — %s\n", work.ID, work.Title)
	if work.Objective != "" {
		fmt.Fprintf(&body, "- 목적: %s\n", work.Objective)
	}
	if work.Acceptance != "" {
		fmt.Fprintf(&body, "- 완료 기준: %s\n", work.Acceptance)
	}
	if work.ChangeScope != "" {
		fmt.Fprintf(&body, "- 선언된 변경 범위: `%s`\n", work.ChangeScope)
	}
	body.WriteString("\n## 검증\n\n")
	if len(results) == 0 {
		body.WriteString("기록된 검증 결과가 없습니다.\n")
	} else {
		body.WriteString("| 게이트 | 결과 | 측정값 | 종료 코드 |\n| --- | --- | --- | --- |\n")
		for _, result := range results {
			fmt.Fprintf(&body, "| %s%s | %s | %s | %d |\n", result.CheckType, optionalMark(result.Required), result.Status, orDash(result.ActualValue), result.ExitCode)
		}
	}
	body.WriteString("\n## 목표 완료 조건\n\n")
	for _, criterion := range criteria {
		fmt.Fprintf(&body, "- %s = %s → %s\n", criterion.Type, criterion.ExpectedValue, criterionLabel(criterion))
	}
	fmt.Fprintf(&body, "\n## 변경 파일 %d개\n\n", len(changes))
	for _, change := range changes {
		fmt.Fprintf(&body, "- `%s` (%s)\n", change.Path, change.ChangeType)
	}
	fmt.Fprintf(&body, "\n---\nGoal-ID: %s\nWork-Item-ID: %s\nRun-ID: %s\nCommit: %s\n", goal.ID, work.ID, commit.RunID, commit.CommitSHA)
	body.WriteString("\n이 브랜치는 격리된 worktree 에서 검증되었습니다. 병합 후에는 통합 검증(`goalforge verify integration`)이 필요합니다.\n")
	pr.Body = body.String()
	return pr, nil
}

func optionalMark(required bool) string {
	if required {
		return ""
	}
	return " (선택)"
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func criterionLabel(status CriterionStatus) string {
	switch status.Status {
	case "MET":
		return "충족 (" + orDash(status.ActualValue) + ")"
	case "UNMET":
		return "기준 미달 (" + orDash(status.ActualValue) + ")"
	case "STALE":
		return "재검증 필요"
	case "WRONG_KIND":
		return "검증 종류 불일치 (" + status.KindMismatch() + ")"
	default:
		return "증거 없음"
	}
}
