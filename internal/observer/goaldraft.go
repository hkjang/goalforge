package observer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/policy"
	"github.com/goalforge/goalforge/internal/prompt"
	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/rrsi"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/verification"
)

// DraftCriterion is one proposed completion condition with the gate that would
// settle it.
type DraftCriterion struct {
	Type          string   `json:"type"`
	ExpectedValue string   `json:"expected_value"`
	Kind          string   `json:"kind"`
	GateCommand   []string `json:"gate_command"`
	WhyItFailsNow string   `json:"why_it_fails_now"`
	// FailsNow records whether running the gate against the current tree
	// actually failed. A gate for work not yet done that passes today is not
	// measuring that work.
	FailsNow bool   `json:"fails_now"`
	Output   string `json:"-"`
}

// GoalDraft is a proposed goal.
type GoalDraft struct {
	Title     string           `json:"title"`
	Objective string           `json:"objective"`
	Criteria  []DraftCriterion `json:"criteria"`
	Refusals  []rrsi.Refusal   `json:"refusals,omitempty"`
	Attempts  int              `json:"attempts"`
}

// Accepted reports whether the draft may be shown as something to confirm.
func (d GoalDraft) Accepted() bool { return len(d.Refusals) == 0 && len(d.Criteria) > 0 }

// Refusal kinds particular to a goal draft.
const (
	// RefusalAlwaysPasses means the gate cannot fail, so it measures nothing.
	RefusalAlwaysPasses = "ALWAYS_PASSES"
	// RefusalPassesAlready means the gate passed against the current tree. A
	// gate for work not yet done that passes today will pass tomorrow, and the
	// criterion it settles is satisfied before anybody writes anything.
	RefusalPassesAlready = "PASSES_ALREADY"
	// RefusalUnjudgeable means the expected value cannot be compared against a
	// measurement.
	RefusalUnjudgeable = "UNJUDGEABLE"
)

// trivialCommands are shells for "always succeed". A gate built from one of
// them is green from the moment it is written and stays green through every
// change, which is the most expensive way to have no gate at all.
var trivialCommands = map[string]bool{
	"true": true, ":": true, "echo": true, "printf": true, "pwd": true, "whoami": true,
}

var criterionName = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)

// draftGateTimeout bounds a proposed gate while it is being tried out. It is
// short because the question here is only whether the command runs and fails,
// not whether a full suite passes.
const draftGateTimeout = 60 * time.Second

func firstLineOf(output string) string {
	output = strings.TrimSpace(output)
	if index := strings.IndexByte(output, '\n'); index >= 0 {
		output = output[:index]
	}
	if len(output) > 160 {
		output = output[:160]
	}
	return output
}

// ScreenDraft checks a proposed goal before anybody is asked to confirm it.
func ScreenDraft(draft GoalDraft) []rrsi.Refusal {
	var refusals []rrsi.Refusal
	if strings.TrimSpace(draft.Title) == "" || strings.TrimSpace(draft.Objective) == "" {
		refusals = append(refusals, rrsi.Refusal{Kind: rrsi.RefusalNoHypothesis,
			Detail: "제목과 목적이 필요합니다"})
	}
	if len(draft.Criteria) == 0 {
		refusals = append(refusals, rrsi.Refusal{Kind: rrsi.RefusalNoHypothesis,
			Detail: "완료 조건이 없습니다 — 아무것도 판정할 수 없는 목표는 목표가 아닙니다"})
	}
	seen := map[string]bool{}
	for _, criterion := range draft.Criteria {
		name := strings.ToLower(strings.TrimSpace(criterion.Type))
		if !criterionName.MatchString(name) {
			refusals = append(refusals, rrsi.Refusal{Kind: rrsi.RefusalNoHypothesis,
				Detail: fmt.Sprintf("%q 는 기준 이름으로 쓸 수 없습니다 — 소문자와 밑줄만", criterion.Type)})
			continue
		}
		if seen[name] {
			refusals = append(refusals, rrsi.Refusal{Kind: rrsi.RefusalNoHypothesis,
				Detail: fmt.Sprintf("%s 기준이 두 번 있습니다", name)})
		}
		seen[name] = true
		if err := policy.ValidGateKind(criterion.Kind); err != nil {
			refusals = append(refusals, rrsi.Refusal{Kind: rrsi.RefusalNoHypothesis,
				Detail: fmt.Sprintf("%s: %v", name, err)})
		}
		if err := policy.ValidateCommand(criterion.GateCommand); err != nil {
			refusals = append(refusals, rrsi.Refusal{Kind: rrsi.RefusalNoHypothesis,
				Detail: fmt.Sprintf("%s: 게이트 명령이 거절되었습니다: %v", name, err)})
			continue
		}
		if len(criterion.GateCommand) > 0 && trivialCommands[baseCommand(criterion.GateCommand[0])] {
			refusals = append(refusals, rrsi.Refusal{Kind: RefusalAlwaysPasses,
				Detail: fmt.Sprintf("%s: %q 는 언제나 통과합니다 — 통과만 하는 게이트는 게이트가 아닙니다",
					name, strings.Join(criterion.GateCommand, " "))})
		}
		// The expected value has to be something a measurement can be compared
		// against. "잘 동작함" is a sentence, not a threshold.
		if expectation := standardsExpectation(criterion.ExpectedValue); expectation == "" {
			refusals = append(refusals, rrsi.Refusal{Kind: RefusalUnjudgeable,
				Detail: fmt.Sprintf("%s: %q 는 측정값과 비교할 수 없습니다 — true, =0, <=200ms 처럼 적으세요",
					name, criterion.ExpectedValue)})
		}
		if strings.TrimSpace(criterion.WhyItFailsNow) == "" {
			refusals = append(refusals, rrsi.Refusal{Kind: rrsi.RefusalNoHypothesis,
				Detail: fmt.Sprintf("%s: 지금 왜 실패하는지 적지 않았습니다", name)})
		}
	}
	return refusals
}

// standardsExpectation returns the normalized expectation, or "" when the
// value cannot be compared against anything.
func standardsExpectation(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	// A bare word is fine when it is an equality a gate can produce — "true",
	// "false", a status. A sentence is not.
	if strings.ContainsAny(trimmed, " \t") && !strings.ContainsAny(trimmed, "<>=") {
		return ""
	}
	return trimmed
}

func baseCommand(command string) string {
	if index := strings.LastIndexByte(command, '/'); index >= 0 {
		command = command[index+1:]
	}
	return strings.ToLower(strings.TrimSpace(command))
}

// VerifyDraftFailsNow runs each proposed gate against the current tree.
//
// A gate for work not yet done must fail. One that passes today will pass
// tomorrow, and the criterion it settles is satisfied before anybody writes
// anything — the goal would be complete at the moment it was created.
//
// This is the only check here that costs anything to run, and it is the only
// one that can tell a gate that measures the work from a gate that measures
// nothing.
func VerifyDraftFailsNow(ctx context.Context, engine *verification.Engine, repository string, draft *GoalDraft) []rrsi.Refusal {
	var refusals []rrsi.Refusal
	for i := range draft.Criteria {
		criterion := &draft.Criteria[i]
		results, _, err := engine.Check(ctx, repository, []verification.Gate{{
			Type: criterion.Type, Command: criterion.GateCommand, Required: true,
			SuccessValue: criterion.ExpectedValue, Kind: criterion.Kind,
			// A gate with no timeout is refused before it runs, and the result
			// then looks exactly like a gate that failed. Found by running
			// this: both a working command and a missing one came back failed
			// because neither was executed.
			Timeout: draftGateTimeout}})
		if err != nil || len(results) == 0 || results[0].ExitCode < 0 {
			// The gate could not be run at all — no exit status came back.
			// That is not the same as a gate that failed: a command that does
			// not exist proves nothing about whether the work is done, and
			// counting it as a failure would let a typo stand in for a
			// measurement.
			detail := "명령을 실행할 수 없었습니다"
			if err != nil {
				detail = err.Error()
			} else if len(results) > 0 && strings.TrimSpace(results[0].Output) != "" {
				detail = firstLineOf(results[0].Output)
			}
			refusals = append(refusals, rrsi.Refusal{Kind: RefusalAlwaysPasses,
				Detail: fmt.Sprintf("%s: 게이트를 돌릴 수 없었습니다: %s", criterion.Type, detail)})
			continue
		}
		criterion.FailsNow = results[0].Status != "PASSED"
		criterion.Output = results[0].Output
		if !criterion.FailsNow {
			refusals = append(refusals, rrsi.Refusal{Kind: RefusalPassesAlready,
				Detail: fmt.Sprintf("%s: 이 게이트가 지금 이미 통과합니다 — 아직 만들지 않은 것을 재고 있지 않습니다 (주장: %s)",
					criterion.Type, criterion.WhyItFailsNow)})
		}
	}
	return refusals
}

// DraftGoal asks a provider for a goal and screens what comes back.
func DraftGoal(ctx context.Context, author Author, req DraftRequest) (GoalDraft, error) {
	var draft GoalDraft
	if author.Provider == nil {
		return draft, errors.New("초안을 쓸 제공자가 없습니다")
	}
	repairs := author.Repairs
	if repairs < 0 {
		repairs = 0
	}
	text := prompt.GoalDraft(req.Topic, req.RepositorySummary, req.ExistingGates)
	for attempt := 0; attempt <= repairs; attempt++ {
		draft.Attempts = attempt + 1
		raw, err := askFor(ctx, author, req, text, prompt.GoalDraftSchema())
		if err != nil {
			return draft, err
		}
		parsed, err := parseDraft(raw)
		if err != nil {
			draft.Refusals = []rrsi.Refusal{{Kind: "MALFORMED", Detail: err.Error()}}
			text = draftRepair(req, draft.Refusals)
			continue
		}
		parsed.Attempts = draft.Attempts
		parsed.Refusals = ScreenDraft(parsed)
		if len(parsed.Refusals) == 0 && req.Engine != nil {
			parsed.Refusals = VerifyDraftFailsNow(ctx, req.Engine, req.Repository, &parsed)
		}
		draft = parsed
		if len(draft.Refusals) == 0 {
			return draft, nil
		}
		text = draftRepair(req, draft.Refusals)
	}
	return draft, nil
}

// DraftRequest is everything a goal draft needs.
type DraftRequest struct {
	Topic             string
	Repository        string
	RepositorySummary string
	ExistingGates     []string
	Engine            *verification.Engine
}

func askFor(ctx context.Context, author Author, req DraftRequest, text, schema string) (string, error) {
	events, err := author.Provider.Start(ctx, provider.RunRequest{RunID: store.NewID("DFT"),
		Prompt: text, WorkDir: req.Repository, Model: author.Model,
		OutputSchema: schema, Ephemeral: true})
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for event := range events {
		switch event.Type {
		case provider.EventMessage, provider.EventCompleted:
			builder.WriteString(event.Message)
		case provider.EventFailed:
			return "", fmt.Errorf("초안 생성이 실패했습니다: %s", event.Message)
		}
	}
	output := strings.TrimSpace(builder.String())
	if output == "" {
		return "", ErrNoProposal
	}
	return output, nil
}

func draftRepair(req DraftRequest, refusals []rrsi.Refusal) string {
	return prompt.GoalDraft(req.Topic, req.RepositorySummary, req.ExistingGates) +
		"\n\n직전 초안이 거절되었다. 아래를 모두 고쳐서 다시 제시하라:\n" + rrsi.Explain(refusals)
}

func parseDraft(raw string) (GoalDraft, error) {
	var draft GoalDraft
	if err := json.Unmarshal([]byte(extractJSON(raw)), &draft); err != nil {
		return draft, fmt.Errorf("구조화된 응답을 읽지 못했습니다: %w", err)
	}
	return draft, nil
}
