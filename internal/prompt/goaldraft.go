package prompt

import (
	"encoding/json"
	"fmt"
	"strings"
)

var goalDraftSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"title", "objective", "criteria", "health"},
	"properties": map[string]any{
		"title":     map[string]any{"type": "string", "minLength": 2},
		"objective": map[string]any{"type": "string", "minLength": 10},
		// Separate from the criteria because the two answer different
		// questions. A completion criterion says whether the goal is done; the
		// health gate says whether a run broke anything. Asked for together
		// and installed together, the completion criteria became required
		// gates that no single work item could satisfy — the first item
		// implements one piece and the end-to-end criterion still fails
		// because nothing else exists yet, so every run failed and a
		// multi-part goal could never progress.
		"health": map[string]any{
			"type": "object", "additionalProperties": false,
			"required":    []string{"type", "expected_value", "kind", "gate_command", "why_it_fails_now"},
			"description": "매 실행 뒤 통과해야 할 게이트 하나. 지금 이미 통과해야 하고, 부분적으로 만들어진 코드도 통과할 수 있어야 한다 (예: go build ./... 또는 npm run typecheck). 완료 조건을 여기 넣으면 목표가 끝날 때까지 모든 실행이 실패한다.",
			"properties": map[string]any{
				"type": map[string]any{"type": "string", "minLength": 2,
					"description": "게이트 이름. 소문자와 밑줄만."},
				"expected_value": map[string]any{"type": "string", "minLength": 1},
				"kind": map[string]any{"type": "string",
					"enum": []string{"build", "test"},
					// Build or test only. A journey or security gate run after
					// every work item is a goal criterion wearing the wrong
					// label, and it would fail every partial run.
					"description": "build 또는 test. 이것은 건강 검사이지 완료 조건이 아니다."},
				"gate_command": map[string]any{"type": "array", "minItems": 1,
					"items": map[string]any{"type": "string"},
					"description": "지금 이 저장소에서 통과하는 명령. 실행 파일과 인자를 각각 하나의 원소로. " +
						`예: ["go","build","./..."]. 셸은 쓸 수 없다 — sh -c "..." 는 거절된다.`},
				"why_it_fails_now": map[string]any{"type": "string", "minLength": 10,
					"description": "이 게이트가 지금 통과하는 이유, 그리고 어떤 변경이 이것을 깨뜨리는가"},
			},
		},
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
						"items": map[string]any{"type": "string"},
						// The shell is blocked by the command policy, and the
						// policy was never told to the side that fills this in:
						// a drafter needing two commands reached for
						// sh -c "a && b" and every criterion was refused.
						"description": "이 기준을 재는 명령. 실행 파일과 인자를 각각 하나의 원소로. " +
							`예: ["go","test","-run","TestRedirect","./..."]. 셸은 쓸 수 없다 — ` +
							`sh -c "..." 나 && 나 파이프는 거절된다. 두 명령이 필요하면 기준을 둘로 나눠라.`},
					"value_pattern": map[string]any{"type": "string",
						// Required in practice for a test runner, because
						// `go test -run ^TestX$ ./pkg` exits zero when the
						// package has no test file — the criterion went green
						// the moment an empty package existed.
						"description": "출력에서 측정값을 뽑는 정규식, 캡처 그룹 하나. 테스트를 돌리는 게이트에는 " +
							`필수다: 걸러낸 검사가 하나도 없어도 go test 는 통과하므로, 그 검사가 실제로 ` +
							`통과했음을 확인해야 한다. 예: --- PASS: (TestRedirect) — 이때 명령에 -v 가 있어야 하고 ` +
							`expected_value 는 TestRedirect 다.`},
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
- **테스트를 돌리는 게이트는 그 검사가 실제로 실행됐음을 확인해야 한다.** go test 는 테스트 파일이
  없는 패키지에서 "[no test files]" 를 찍고 exit 0 한다. 걸러낸 실행도 맞는 검사가 없으면
  "no tests to run" 과 함께 통과한다. 그러면 빈 패키지만 만들어도 그 기준이 충족된 것으로 집계되고,
  아무도 구현하지 않은 기능에 "목표 완료" 가 보고된다.
  그래서 -v 를 붙이고 value_pattern 으로 그 검사의 PASS 를 확인하라:
    gate_command: ["go","test","-count=1","-v","-run","^TestRedirect$","./shortener"]
    value_pattern: "--- PASS: (TestRedirect)"
    expected_value: "TestRedirect"
  value_pattern 은 걸러낸 이름을 담아야 한다. 다른 줄에 맞는 패턴은 검사가 없어도 통과한다.
- 게이트 명령에 **셸을 쓸 수 없다.** sh -c "..." 와 && 와 파이프는 거절된다. 실행 파일과 인자를
  각각 하나의 원소로 적어라: ["go","test","-run","TestRedirect","./..."].
  두 명령이 필요하면 그것은 기준이 둘이라는 뜻이다. 하나의 게이트는 하나를 재야 판정을 귀속할 수 있다.
- 기준은 6개를 넘기지 마라. 판정할 수 없을 만큼 많은 조건은 아무도 판정하지 않는다.
- health 게이트는 **지금 이미 통과해야** 하고, 부분적으로 만들어진 코드도 통과할 수 있어야 한다.
  좋음: go build ./...        좋음: npm run typecheck        좋음: go vet ./...
  나쁨: go test -run TestJourney  (완료 조건이다 — 목표가 끝날 때까지 모든 실행을 막는다)
  이것은 매 작업 항목 뒤에 돌아가 "이번 변경이 무언가를 깨뜨렸는가" 를 묻는 게이트다.
  완료 조건과 반대 방향이다: 완료 조건은 지금 실패해야 하고, health 는 지금 통과해야 한다.`
