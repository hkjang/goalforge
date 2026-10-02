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
		"items": ideaItemSchema(),
	}},
}

func ideaItemSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"title", "expected_change_scope", "risk", "goal_contribution", "user_value", "operational_need", "feasibility", "risk_reduction", "difficulty", "scope_expansion"},
		"properties": map[string]any{
			"title": map[string]any{"type": "string"},
			// The format is stated in the schema, not asked for in prose. This
			// field is compared against file paths as a list of glob patterns,
			// so a sentence matches nothing — and a work item whose scope
			// matches nothing can never run: every file the session writes is
			// reported as out of scope and the run is refused.
			//
			// Asked for as a sentence, it came back as one. The pattern is
			// what makes that impossible rather than unlikely.
			"expected_change_scope": map[string]any{"type": "string",
				"pattern":     `^[^\s:;"'()]+(,[^\s:;"'()]+)*$`,
				"description": "바꿀 파일 경로나 glob 의 쉼표 구분 목록. 예: internal/server/handler.go 또는 internal/store/**,cmd/app/main.go — 설명 문장이 아니다. 공백·콜론이 들어가면 거절된다."},
			"risk":              map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
			"goal_contribution": scoreSchema(), "user_value": scoreSchema(), "operational_need": scoreSchema(),
			"feasibility": scoreSchema(), "risk_reduction": scoreSchema(), "difficulty": scoreSchema(),
			"scope_expansion": map[string]any{"type": "boolean"},
		},
	}
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
- 발견한 문제점마다 근거가 되는 파일을 예상 변경 범위에 적는다.
%s`, goal.Title, goal.Objective, renderBacklog(existing), discoveryRules)
}

const discoveryRules = `- 파일을 수정하거나 명령으로 저장소 상태를 변경하지 않는다.
- 기존 목록과 의미적으로 중복된 아이디어를 만들지 않는다.
- 범위를 확대하는 제안은 scope_expansion=true로 표시한다.
- expected_change_scope 는 **바꿀 파일 경로나 glob 의 쉼표 구분 목록**이다. 설명 문장이 아니다.
  좋음: internal/server/handler.go        좋음: internal/store/**,cmd/app/main.go
  나쁨: handler.go 신설: JSON 본문 파싱…   (문장은 어떤 파일과도 맞지 않아 그 작업은 실행될 수 없다)
- 각 점수는 0~100이다.
- 지정된 JSON 스키마만 반환한다.`

func renderBacklog(existing []model.WorkItem) string {
	var backlog strings.Builder
	for _, item := range existing {
		fmt.Fprintf(&backlog, "- [%s] %s\n", item.Status, item.Title)
	}
	return backlog.String()
}
