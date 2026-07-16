package prompt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goalforge/goalforge/internal/model"
)

var replanSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"gaps", "stale_items"},
	"properties": map[string]any{
		"gaps": map[string]any{
			"type": "array", "maxItems": 5,
			"items": ideaItemSchema(),
		},
		"stale_items": map[string]any{
			"type": "array", "maxItems": 10,
			"items": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"id", "reason"},
				"properties": map[string]any{
					"id":     map[string]any{"type": "string"},
					"reason": map[string]any{"type": "string"},
				},
			},
		},
	},
}

func ReplanSchema() string {
	raw, _ := json.Marshal(replanSchema)
	return string(raw)
}

// Replan drives the REPLAN_GOAL task: compare the goal and its completion
// criteria against what the repository actually implements, then propose gap
// work items and flag backlog entries that no longer serve the goal.
func Replan(goal model.Goal, existing []model.WorkItem) string {
	var criteria strings.Builder
	for _, criterion := range goal.Criteria {
		fmt.Fprintf(&criteria, "- %s = %s\n", criterion.Type, criterion.ExpectedValue)
	}
	var backlog strings.Builder
	for _, item := range existing {
		fmt.Fprintf(&backlog, "- [%s] %s: %s (범위: %s)\n", item.Status, item.ID, item.Title, item.ChangeScope)
	}
	return fmt.Sprintf(`프로젝트 저장소를 읽기 전용으로 분석하여 현재 구현과 목표 간 차이를 평가하고 백로그를 재구성하라.

목표:
%s

목표 설명:
%s

완료 조건:
%s
현재 백로그 (ID 포함):
%s
수행할 분석:
1. 완료 조건 중 현재 구현이 충족하지 못하는 항목을 찾아 이를 메우는 작업을 gaps로 제시한다.
2. 목표에 더 이상 기여하지 않거나 이미 구현된 내용과 중복된 백로그 항목을 stale_items로 지목하고 이유를 명시한다.
3. stale_items의 id는 반드시 위 백로그 목록의 ID를 그대로 사용한다.
4. 진행 중(IN_PROGRESS)이거나 완료(DONE)된 항목은 지목하지 않는다.

규칙:
%s`, goal.Title, goal.Objective, criteria.String(), backlog.String(), discoveryRules)
}
