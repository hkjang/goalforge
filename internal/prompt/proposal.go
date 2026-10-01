package prompt

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/rrsi"
)

var proposalSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"edits"},
	"properties": map[string]any{"edits": map[string]any{
		"type": "array", "minItems": 1, "maxItems": 4,
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			// The hypothesis is required by the schema rather than asked for
			// in prose. A model given an optional field for "why" will
			// sometimes fill it, and an edit without one cannot be falsified —
			// which is the whole mechanism that stops the search re-drawing
			// the same idea.
			"required": []string{"component", "hypothesis", "detail"},
			"properties": map[string]any{
				"component": map[string]any{"type": "string", "enum": rrsi.Components},
				"hypothesis": map[string]any{"type": "string", "minLength": 10,
					"description": "이 변경이 무엇을 개선할 것이며 왜 그런지. 측정으로 반증 가능해야 한다."},
				"detail": map[string]any{"type": "string", "minLength": 5,
					"description": "무엇을 어떻게 바꾸는지"},
			},
		},
	}},
}

// ProposalSchema is the structured output a proposal must take.
func ProposalSchema() string {
	raw, _ := json.Marshal(proposalSchema)
	return string(raw)
}

// Proposal asks for the next configuration change to try.
//
// Everything that makes this a regularized search rather than a guess is in
// the context, not the instructions: the edits already tried and what they
// measured, the explanations that did not hold, the components nothing has
// reached for, and this round's budget. A proposer given only the current
// configuration and "make it better" will draw the most plausible idea, which
// is the one that was tried first and failed first.
func Proposal(direction rrsi.Direction, history rrsi.History, configuration string, caseCount int) string {
	var sections []string
	sections = append(sections, fmt.Sprintf(`이 프로젝트의 실행 구성을 한 번 바꿔 성공률을 올리려 한다.
바꿀 수 있는 부분: %s

현재 구성:
%s`, strings.Join(rrsi.Components, ", "), configuration))
	sections = append(sections, "이번 회차 지침:\n"+direction.Summary)
	if tried := renderHistory(history); tried != "" {
		sections = append(sections, "지금까지 시도한 것과 측정 결과:\n"+tried)
	}
	if avoid := renderFalsified(direction.Avoid); avoid != "" {
		sections = append(sections, "이미 시험해서 성립하지 않은 설명 — 다시 제시하지 말 것:\n"+avoid)
	}
	if len(direction.Explore) > 0 {
		sections = append(sections, "이번 회차는 다음 구성 중 하나를 반드시 건드려야 한다: "+
			strings.Join(direction.Explore, ", "))
	}
	if len(direction.Prune) > 0 {
		sections = append(sections, "다음 구성은 최근 제안들이 이득을 내지 못했다. 늘리기보다 덜어 내는 쪽을 고려하라: "+
			strings.Join(direction.Prune, ", "))
	}
	sections = append(sections, fmt.Sprintf(proposalRules, direction.Budget, caseCount))
	return strings.Join(sections, "\n\n")
}

const proposalRules = `규칙:
- 편집은 최대 %d개. 한도를 넘기면 어떤 편집이 측정을 만들어 냈는지 말할 수 없다.
- 편집마다 반증 가능한 가설을 적어라. "더 나아질 것이다" 는 가설이 아니다.
- 평가 사례를 이름으로 지목하거나 특정 사례에만 맞는 처리를 넣지 마라. 평가 사례는 %d개이고,
  그중 하나에 맞춘 변경은 제품이 나아지지 않아도 점수를 올린다. 그런 제안은 측정 전에 거절된다.
- 비밀값을 넣지 마라.
- 이미 성립하지 않은 것으로 밝혀진 설명을 다시 쓰지 마라. 같은 말을 다르게 적은 것도 같은 설명이다.`

// renderHistory shows what was tried and what it measured.
//
// Measured outcomes come first and refusals are summarized rather than listed.
// A wall of screen refusals is a feedback loop — the proposer reads its own
// rejected attempts and writes more of the same — while the measurements are
// the only part that says anything about the product.
func renderHistory(history rrsi.History) string {
	var lines []string
	refused := 0
	for _, record := range history {
		if record.ScreenRefusal != "" {
			refused++
			continue
		}
		for _, edit := range record.Edits {
			lines = append(lines, fmt.Sprintf("- [%s] %s → %s (성공률 %+.1f%%p, 비용 %+.0f%%)%s",
				edit.Component, edit.Hypothesis, verdictWord(record.Verdict),
				record.ScoreDelta*100, record.CostDelta*100, acceptedMark(record.Accepted)))
		}
	}
	if refused > 0 {
		lines = append(lines, fmt.Sprintf("- (심사에서 거절되어 측정되지 않은 제안 %d건)", refused))
	}
	return strings.Join(lines, "\n")
}

func verdictWord(verdict string) string {
	switch verdict {
	case rrsi.VerdictBetter:
		return "개선됨"
	case rrsi.VerdictNoWorse:
		return "차이가 노이즈 안"
	case rrsi.VerdictNotShown:
		return "개선을 보이지 못함"
	}
	return "판정 불가"
}

func acceptedMark(accepted bool) string {
	if accepted {
		return " [채택됨]"
	}
	return ""
}

func renderFalsified(avoid map[string][]string) string {
	components := make([]string, 0, len(avoid))
	for component := range avoid {
		components = append(components, component)
	}
	sort.Strings(components)
	var lines []string
	for _, component := range components {
		for _, hypothesis := range avoid[component] {
			lines = append(lines, fmt.Sprintf("- [%s] %s", component, hypothesis))
		}
	}
	return strings.Join(lines, "\n")
}
