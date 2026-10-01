package prompt

import (
	"encoding/json"
	"fmt"
	"strings"
)

var goalDraftSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"title", "objective", "criteria"},
	"properties": map[string]any{
		"title":     map[string]any{"type": "string", "minLength": 2},
		"objective": map[string]any{"type": "string", "minLength": 10},
		"criteria": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 6,
			"items": map[string]any{
				"type": "object", "additionalProperties": false,
				// The gate is required alongside the criterion. A criterion
				// with no way to settle it is a sentence somebody will argue
				// about later, and asking for them separately produces exactly
				// that: a list of aspirations and a list of scripts that do
				// not meet.
				"required": []string{"type", "expected_value", "kind", "gate_command", "why_it_fails_now"},
				"properties": map[string]any{
					"type": map[string]any{"type": "string", "minLength": 2,
						"description": "기준 이름. 소문자와 밑줄만."},
					"expected_value": map[string]any{"type": "string", "minLength": 1,
						"description": "충족 기준. 수치는 방향과 단위를 함께: <=200ms, >=99%, =0, true"},
					"kind": map[string]any{"type": "string",
						"enum":        []string{"build", "test", "integration", "journey", "security", "performance"},
						"description": "어떤 종류의 검증이라야 이 기준을 충족시킬 수 있는가"},
					"gate_command": map[string]any{"type": "array", "minItems": 1,
						"items":       map[string]any{"type": "string"},
						"description": "이 기준을 재는 명령. 인자 배열로."},
					"why_it_fails_now": map[string]any{"type": "string", "minLength": 10,
						"description": "아직 구현되지 않았으므로 이 명령이 지금 실패하는 이유"},
				},
			},
		},
	},
}

// GoalDraftSchema is the structured output a goal draft must take.
func GoalDraftSchema() string {
	raw, _ := json.Marshal(goalDraftSchema)
	return string(raw)
}

// GoalDraft asks for completion criteria and the gates that would settle them.
//
// Criteria and gates are asked for together on purpose. Asked separately, a
// model produces a list of aspirations and a list of scripts that do not meet,
// and the gap is discovered when somebody tries to judge the work.
//
// The draft is a proposal. A person confirms it, because a machine that sets
// its own bar has not been measured against anything — it has agreed with
// itself.
func GoalDraft(topic, repository string, existing []string) string {
	var sections []string
	sections = append(sections, fmt.Sprintf(`주제를 판정 가능한 완료 조건과, 그것을 정산할 검증 게이트로 바꿔라.

주제:
%s

저장소 구조:
%s`, topic, repository))
	if len(existing) > 0 {
		sections = append(sections, "이미 설정된 게이트:\n- "+strings.Join(existing, "\n- "))
	}
	sections = append(sections, goalDraftRules)
	return strings.Join(sections, "\n\n")
}

const goalDraftRules = `규칙:
- 기준마다 그것을 재는 명령을 함께 내라. 재는 방법이 없는 기준은 나중에 말다툼할 문장이다.
- 수치 기준은 방향과 단위를 함께 적어라. "200" 은 "200 이상" 으로 읽힌다. 지연 상한은 "<=200ms" 다.
- kind 는 그 기준을 무엇이 충족시킬 수 있는지다. 화면이 동작하는지는 build 가 아니라 journey 가 답한다.
- 게이트 명령은 지금 실패해야 한다. 아직 만들지 않은 것을 재는 명령이 지금 통과한다면 그 명령은
  그것을 재고 있지 않다. why_it_fails_now 에 왜 지금 실패하는지 적어라. 그 설명이 틀리면 거절된다.
- echo, true, exit 0 처럼 항상 통과하는 명령을 쓰지 마라. 통과만 하는 게이트는 게이트가 아니다.
- 기준은 6개를 넘기지 마라. 판정할 수 없을 만큼 많은 조건은 아무도 판정하지 않는다.`
