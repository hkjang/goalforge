package standards

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Evidence is one observed fact behind an assessment.
//
// Observed and inferred are separate fields rather than a confidence number
// because they call for different actions: an inference is something to go and
// check, and a number between 0 and 1 lets an inference average itself into
// looking like an observation.
type Evidence struct {
	// Kind matches a criterion's evidence_required entry — commit_sha, route,
	// browser_test_result, screenshot, build_log, and so on.
	Kind string `json:"kind"`
	// Detail is the evidence itself. An entry with a kind and nothing in it is
	// a ticked box, and a ticked box is not evidence — it is checked
	// separately from Observed, which only says where a claim came from.
	Detail string `json:"detail"`
	// Observed is true when this came from running or reading the thing, and
	// false when it was concluded from something else.
	Observed bool `json:"observed"`
}

// Assessment is where one criterion stands in one repository at one commit.
type Assessment struct {
	ProjectID  string     `json:"project_id"`
	StandardID string     `json:"standard_id"`
	Revision   int        `json:"revision"`
	CommitSHA  string     `json:"commit_sha"`
	Result     string     `json:"result"`
	Detail     string     `json:"detail"`
	Evidence   []Evidence `json:"evidence"`
	AssessedAt time.Time  `json:"assessed_at"`
	// ToolVersion identifies what produced this, so a result can be re-judged
	// when the observer itself changes.
	ToolVersion string `json:"tool_version"`
}

// ErrUnsupportedResult is returned when a result is not one of the five.
type ErrUnsupportedResult struct{ Result string }

func (e ErrUnsupportedResult) Error() string {
	return fmt.Sprintf("%q is not a result: use MET, UNMET, PARTIAL, UNKNOWN or NOT_APPLICABLE", e.Result)
}

// Judge settles what result an assessment is allowed to carry.
//
// It exists because the tempting failure is upward. A file exists, a README
// says the feature is there, the route is in the router — and the criterion
// gets marked MET without anything having been run. That is the one direction
// the mistake is expensive in: UNMET files work that turns out to be
// unnecessary, and MET ships something nobody checked.
func Judge(standard Standard, proposed string, evidence []Evidence, excepted bool) (string, string, error) {
	switch proposed {
	case ResultMet, ResultUnmet, ResultPartial, ResultUnknown, ResultNotApplicable:
	default:
		return "", "", ErrUnsupportedResult{Result: proposed}
	}
	if proposed == ResultNotApplicable && !excepted {
		// NOT_APPLICABLE is a decision, and a decision needs somebody to have
		// made it. Without a recorded exception it is an observer deciding a
		// criterion does not count, which is the one judgement it may not make.
		return ResultUnknown, "적용 제외는 기록된 예외가 있어야 합니다 — 관측기가 스스로 제외할 수 없습니다", nil
	}
	if proposed != ResultMet {
		return proposed, "", nil
	}
	missing := missingEvidence(standard, evidence)
	if len(missing) > 0 {
		return ResultUnknown, fmt.Sprintf("%s 근거가 없습니다 — 충족으로 올릴 수 없습니다", strings.Join(missing, ", ")), nil
	}
	if inferred := inferredKinds(standard, evidence); len(inferred) > 0 {
		// Every required kind is present but some of it was concluded rather
		// than observed. That is an investigation, not a pass.
		return ResultUnknown, fmt.Sprintf("%s 근거가 실행·관측이 아니라 추정입니다", strings.Join(inferred, ", ")), nil
	}
	return ResultMet, "", nil
}

func missingEvidence(standard Standard, evidence []Evidence) []string {
	present := map[string]bool{}
	for _, item := range evidence {
		if strings.TrimSpace(item.Detail) != "" {
			present[strings.ToLower(strings.TrimSpace(item.Kind))] = true
		}
	}
	var missing []string
	for _, required := range standard.EvidenceRequired {
		if !present[strings.ToLower(strings.TrimSpace(required))] {
			missing = append(missing, required)
		}
	}
	sort.Strings(missing)
	return missing
}

func inferredKinds(standard Standard, evidence []Evidence) []string {
	observed := map[string]bool{}
	for _, item := range evidence {
		kind := strings.ToLower(strings.TrimSpace(item.Kind))
		if item.Observed {
			observed[kind] = true
		}
	}
	var inferred []string
	for _, required := range standard.EvidenceRequired {
		if !observed[strings.ToLower(strings.TrimSpace(required))] {
			inferred = append(inferred, required)
		}
	}
	sort.Strings(inferred)
	return inferred
}

// DedupKey identifies the same finding across supply cycles.
//
// It is built from project, criterion, defect kind and target scope rather
// than from the title, because a generator asked for a new idea will happily
// produce a new sentence about the same defect every cycle. Title similarity
// is a fallback for the cases this key cannot see, not the primary test.
func DedupKey(projectID, standardID, defectKind, targetScope string) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(projectID)),
		strings.ToUpper(strings.TrimSpace(standardID)),
		strings.ToLower(strings.TrimSpace(defectKind)),
		strings.ToLower(strings.TrimSpace(targetScope)),
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, "\x00"))))[:32]
}

func contentDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))[:32]
}
