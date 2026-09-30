package rrsi

import (
	"fmt"
	"regexp"
	"strings"
)

// Refusal is one reason a candidate must not be measured.
//
// Screening happens before evaluation because evaluation is the expensive
// part. A candidate that special-cases the evaluation set will score well, and
// the score is the thing that would have caught it.
type Refusal struct {
	Kind, Detail string
}

// Refusal kinds.
const (
	// RefusalLeakage means the change names something from the evaluation set.
	// The harness is evolved against the very cases it is measured on, so a
	// change that mentions one of them scores better without the product being
	// better — and the measurement cannot tell the difference.
	RefusalLeakage = "LEAKAGE"
	// RefusalBudget means the candidate bundles more independent edits than
	// this round allows, which makes the measurement unattributable.
	RefusalBudget = "BUDGET"
	// RefusalFalsified means this explanation was tested and did not hold.
	RefusalFalsified = "FALSIFIED"
	// RefusalNoHypothesis means an edit says what it changes and not what it
	// expects, which cannot be falsified and so cannot be learned from.
	RefusalNoHypothesis = "NO_HYPOTHESIS"
	// RefusalSecret means a credential appeared in the change.
	RefusalSecret = "SECRET"
)

// credentialPatterns catch a secret pasted into a configuration change. They
// are here rather than in a review checklist because a screen that runs is
// worth more than a rule somebody is supposed to remember.
var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`),
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|password|secret)\s*[:=]\s*["']?[A-Za-z0-9/_\-+]{12,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// ScreenInput is everything the screen needs to judge a candidate.
type ScreenInput struct {
	Edits []Edit
	// Budget is this round's edit budget; zero means unlimited.
	Budget int
	// CaseNames are the evaluation cases this project is measured on. A change
	// that mentions one of them is tuning to the test.
	CaseNames []string
	History   History
}

// minCaseNameLength is how short an evaluation case name may be before it is
// too generic to screen on.
//
// A case called "api" would refuse every change that mentions an API. The
// screen is worth having only while its refusals are about leakage, and one
// bad refusal teaches people to pass a flag.
const minCaseNameLength = 4

// Screen decides whether a candidate may be measured.
//
// Every refusal is returned rather than the first, because a candidate is sent
// back to be repaired and a repair that fixes one objection only to hit the
// next wastes a round per objection.
func Screen(input ScreenInput) []Refusal {
	var refusals []Refusal
	if len(input.Edits) == 0 {
		return []Refusal{{Kind: RefusalNoHypothesis, Detail: "변경이 없습니다"}}
	}
	if input.Budget > 0 && len(input.Edits) > input.Budget {
		refusals = append(refusals, Refusal{Kind: RefusalBudget,
			Detail: fmt.Sprintf("편집 %d개는 이번 회차 한도 %d개를 넘습니다 — 묶으면 측정이 어느 편집에 대한 것인지 말할 수 없습니다",
				len(input.Edits), input.Budget)})
	}
	falsified := input.History.Falsified()
	for _, edit := range input.Edits {
		if strings.TrimSpace(edit.Hypothesis) == "" {
			refusals = append(refusals, Refusal{Kind: RefusalNoHypothesis,
				Detail: fmt.Sprintf("%s 편집에 기대가 없습니다 — 반증할 수 없는 변경은 배울 것이 없습니다", edit.Component)})
		}
		if !contains(Components, edit.Component) {
			refusals = append(refusals, Refusal{Kind: RefusalNoHypothesis,
				Detail: fmt.Sprintf("%q 는 알려진 구성이 아닙니다 — 이름을 새로 지으면 모든 제안이 새로워 보입니다", edit.Component)})
		}
		for _, already := range falsified[edit.Component] {
			if hypothesisKey(edit.Component, already) == hypothesisKey(edit.Component, edit.Hypothesis) {
				refusals = append(refusals, Refusal{Kind: RefusalFalsified,
					Detail: fmt.Sprintf("%s: 이 설명은 이미 시험해서 성립하지 않았습니다 — %q", edit.Component, already)})
			}
		}
		text := edit.Hypothesis + "\n" + edit.Detail
		for _, name := range input.CaseNames {
			if len(strings.TrimSpace(name)) < minCaseNameLength {
				// Too generic to screen on. Refusing every change that
				// mentions a three-letter word would make the screen the thing
				// people work around.
				continue
			}
			if strings.Contains(strings.ToLower(text), strings.ToLower(strings.TrimSpace(name))) {
				refusals = append(refusals, Refusal{Kind: RefusalLeakage,
					Detail: fmt.Sprintf("%s: 평가 사례 %q 를 직접 언급합니다 — 측정 대상에 맞춘 변경은 제품이 나아지지 않아도 점수가 오르고, 점수로는 그 차이를 알 수 없습니다",
						edit.Component, strings.TrimSpace(name))})
			}
		}
		for _, pattern := range credentialPatterns {
			if pattern.MatchString(text) {
				refusals = append(refusals, Refusal{Kind: RefusalSecret,
					Detail: fmt.Sprintf("%s 편집에 비밀값처럼 보이는 문자열이 있습니다", edit.Component)})
				break
			}
		}
	}
	return refusals
}

// Passed reports whether a screen produced no refusals.
func Passed(refusals []Refusal) bool { return len(refusals) == 0 }

// Explain renders refusals for a person or a repair round.
func Explain(refusals []Refusal) string {
	lines := make([]string, 0, len(refusals))
	for _, refusal := range refusals {
		lines = append(lines, refusal.Kind+": "+refusal.Detail)
	}
	return strings.Join(lines, "\n")
}
