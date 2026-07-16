package prompt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goalforge/goalforge/internal/model"
)

var ideasSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"ideas"},
	"properties": map[string]any{"ideas": map[string]any{
		"type": "array", "maxItems": 5,
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"title", "expected_change_scope", "risk", "goal_contribution", "user_value", "operational_need", "feasibility", "risk_reduction", "difficulty", "scope_expansion"},
			"properties": map[string]any{
				"title": map[string]any{"type": "string"}, "expected_change_scope": map[string]any{"type": "string"},
				"risk":              map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
				"goal_contribution": scoreSchema(), "user_value": scoreSchema(), "operational_need": scoreSchema(),
				"feasibility": scoreSchema(), "risk_reduction": scoreSchema(), "difficulty": scoreSchema(),
				"scope_expansion": map[string]any{"type": "boolean"},
			},
		},
	}},
}

func scoreSchema() map[string]any {
	return map[string]any{"type": "number", "minimum": 0, "maximum": 100}
}

func IdeasSchema() string {
	raw, _ := json.Marshal(ideasSchema)
	return string(raw)
}

func Ideas(goal model.Goal, existing []model.WorkItem) string {
	return fmt.Sprintf(`프로젝트 저장소를 읽기 전용으로 분석하여 현재 목표에 직접 기여하는 중복되지 않은 아이디어를 최대 5개 제시하라.

목표:
%s

목표 설명:
%s

기존 및 완료/보류 작업:
%s
규칙:
%s`, goal.Title, goal.Objective, renderBacklog(existing), discoveryRules)
}

// Audit drives the AUDIT_AND_IMPROVE task: a read-only inspection of the
// repository across quality, security, performance, UI/UX, and operability,
// returning improvement candidates in the same schema as idea discovery.
func Audit(goal model.Goal, existing []model.WorkItem) string {
	return fmt.Sprintf(`프로젝트 저장소를 읽기 전용으로 감사하여 품질, 보안, 성능, UI·UX, 운영성 관점의 문제점을 찾고 개선 작업을 최대 5개 제시하라.

목표:
%s

목표 설명:
%s

기존 및 완료/보류 작업:
%s
감사 관점:
- 품질: 오류 처리 누락, 회귀 위험, 테스트 공백
- 보안: 비밀정보 노출, 입력 검증 부재, 과도한 권한
- 성능: 불필요한 반복 작업, 자원 누수, 병목
- UI·UX: 출력 일관성, 오류 메시지 명확성
- 운영성: 로그·지표 공백, 복구 절차 부재, 설정 경직성

규칙:
- 발견한 문제점마다 구체적인 근거 파일이나 동작을 예상 변경 범위에 명시한다.
%s`, goal.Title, goal.Objective, renderBacklog(existing), discoveryRules)
}

const discoveryRules = `- 파일을 수정하거나 명령으로 저장소 상태를 변경하지 않는다.
- 기존 목록과 의미적으로 중복된 아이디어를 만들지 않는다.
- 범위를 확대하는 제안은 scope_expansion=true로 표시한다.
- 각 점수는 0~100이며 구체적인 예상 변경 범위를 작성한다.
- 지정된 JSON 스키마만 반환한다.`

func renderBacklog(existing []model.WorkItem) string {
	var backlog strings.Builder
	for _, item := range existing {
		fmt.Fprintf(&backlog, "- [%s] %s\n", item.Status, item.Title)
	}
	return backlog.String()
}
