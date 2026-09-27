package prompt

import (
	"fmt"
	"strings"

	"github.com/goalforge/goalforge/internal/model"
)

type Budget struct {
	TokenLimit, TokensUsed    int64
	CostLimitUSD, CostUsedUSD float64
}

func Execution(goal model.Goal, work model.WorkItem, budget Budget) string {
	criteria := make([]string, 0, len(goal.Criteria))
	for _, c := range goal.Criteria {
		criteria = append(criteria, fmt.Sprintf("- %s = %s", c.Type, c.ExpectedValue))
	}
	return strings.TrimSpace(fmt.Sprintf(`프로젝트 목표:
%s

목표 설명:
%s

완료 조건:
%s

현재 단일 작업:
ID: %s
제목: %s
유형: %s
위험도: %s
허용 변경 범위: %s

제약사항:
- 현재 작업 범위를 벗어난 기능을 임의로 추가하지 않는다.
- 기존 구현을 먼저 분석하고 중복 기능을 만들지 않는다.
- 한 번에 이 작업 하나만 수행한다.
- 민감정보와 기존 사용자 변경 사항을 수정하지 않는다.
- 완료 전 관련 빌드와 테스트를 실행한다.
- 테스트 실패를 숨기거나 성공으로 보고하지 않는다.
- 변경 파일과 실행한 명령을 구조화해 반환한다.
- 남은 작업이 있으면 다음 행동을 한 개만 제시한다.

프로젝트 예산:
토큰 %d / %d, 비용 %.4f / %.4f USD`, goal.Title, goal.Objective, strings.Join(criteria, "\n"), work.ID, work.Title, work.Type, work.Risk, work.ChangeScope, budget.TokensUsed, budget.TokenLimit, budget.CostUsedUSD, budget.CostLimitUSD))
}

// ContextSection is a named group of context lines with their provenance.
type ContextSection struct {
	Heading string
	Items   []ContextLine
}

// ContextLine is one assembled fact. Source and AsOf travel with it so the
// session can tell current fact from stale context instead of treating every
// line as equally true now.
type ContextLine struct {
	Title, Body, Source, AsOf string
}

// WithContext appends the assembled work-item context to an execution prompt.
// The instruction to treat decisions as settled is the point: without it a new
// session re-derives the architecture and sometimes reverses a choice that was
// already made deliberately.
func WithContext(rendered string, sections []ContextSection) string {
	var builder strings.Builder
	builder.WriteString(rendered)
	wrote := false
	for _, section := range sections {
		if len(section.Items) == 0 {
			continue
		}
		wrote = true
		builder.WriteString("\n\n" + section.Heading + ":")
		for _, item := range section.Items {
			builder.WriteString("\n- " + item.Title)
			if item.Source != "" || item.AsOf != "" {
				builder.WriteString(" [" + strings.TrimSpace(item.Source+" "+item.AsOf) + "]")
			}
			if item.Body != "" {
				builder.WriteString("\n  " + strings.ReplaceAll(item.Body, "\n", "\n  "))
			}
		}
	}
	if !wrote {
		return rendered
	}
	builder.WriteString("\n\n위 맥락 사용 규칙:\n" +
		"- 설계 결정은 이미 합의된 것이다. 더 나은 방법이 보이면 임의로 바꾸지 말고 다음 행동으로 제안한다.\n" +
		"- 이전 실패는 같은 수정을 반복하지 않기 위한 것이다. 같은 접근을 다시 시도하려면 무엇이 달라졌는지 밝힌다.\n" +
		"- 검증 명령과 기준은 완화하지 않는다.")
	return builder.String()
}
