package observer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/goalforge/goalforge/internal/prompt"
	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/rrsi"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// Author asks a provider for the next configuration change and puts the answer
// through the screen before anything is spent measuring it.
//
// The proposer is a model, so what it writes is the product of what it is
// shown. Everything that regularizes the search is in the context — the edits
// already tried with their measured outcomes, the explanations that did not
// hold, this round's budget — and the screen is what enforces the rules the
// context asks for, because asking is not enforcing.
type Author struct {
	Provider provider.Provider
	Model    string
	// Repairs is how many times a refused proposal may be sent back with the
	// objections. Bounded, because a proposer that cannot satisfy the screen
	// in a few attempts is not going to on the tenth, and every attempt costs.
	Repairs int
}

// Proposal is what an author produced.
type Proposal struct {
	Edits []rrsi.Edit
	// Attempts is how many times the model was asked, including repairs.
	Attempts int
	// Refusals are the objections that stopped the last attempt. A proposal
	// with refusals is one that was not accepted, and the caller must not
	// measure it.
	Refusals []rrsi.Refusal
	Raw      string
}

// Accepted reports whether the screen passed this proposal.
func (p Proposal) Accepted() bool { return len(p.Refusals) == 0 && len(p.Edits) > 0 }

// ErrNoProposal means the model produced nothing usable.
var ErrNoProposal = errors.New("제안을 얻지 못했습니다")

// Write asks for a proposal and repairs it up to the configured bound.
func (a Author) Write(ctx context.Context, req AuthorRequest) (Proposal, error) {
	var result Proposal
	if a.Provider == nil {
		return result, errors.New("제안을 쓸 제공자가 없습니다")
	}
	repairs := a.Repairs
	if repairs < 0 {
		repairs = 0
	}
	text := prompt.Proposal(req.Direction, req.History, req.Configuration, req.CaseCount)
	for attempt := 0; attempt <= repairs; attempt++ {
		result.Attempts = attempt + 1
		raw, err := a.ask(ctx, req, text)
		if err != nil {
			return result, err
		}
		result.Raw = raw
		edits, err := parseProposal(raw)
		if err != nil {
			// A malformed answer is sent back the same way a refused one is:
			// the model is told what was wrong rather than being asked again
			// into the same silence.
			result.Refusals = []rrsi.Refusal{{Kind: "MALFORMED", Detail: err.Error()}}
			text = repairPrompt(req, result.Refusals)
			continue
		}
		result.Edits = edits
		result.Refusals = rrsi.Screen(rrsi.ScreenInput{Edits: edits, Budget: req.Direction.Budget,
			CaseNames: req.CaseNames, History: req.History})
		if len(result.Refusals) == 0 {
			return result, nil
		}
		text = repairPrompt(req, result.Refusals)
	}
	return result, nil
}

// AuthorRequest is everything a proposal needs.
type AuthorRequest struct {
	ProjectID     string
	Direction     rrsi.Direction
	History       rrsi.History
	Configuration string
	CaseNames     []string
	CaseCount     int
	WorkDir       string
}

func (a Author) ask(ctx context.Context, req AuthorRequest, text string) (string, error) {
	// Ephemeral and read-only: a proposer is reading the configuration to
	// write a suggestion about it, not editing anything. The change itself is
	// applied by whoever accepts the proposal, after it has been measured.
	// A run ID even though nothing is recorded against it: providers use it to
	// name their own session and refuse without one. Found by running this
	// against a real adapter rather than a test double, which accepted
	// whatever it was handed.
	events, err := a.Provider.Start(ctx, provider.RunRequest{RunID: store.NewID("PRP"),
		Prompt: text, WorkDir: req.WorkDir, Model: a.Model,
		OutputSchema: prompt.ProposalSchema(), Ephemeral: true})
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for event := range events {
		switch event.Type {
		case provider.EventMessage, provider.EventCompleted:
			builder.WriteString(event.Message)
		case provider.EventFailed:
			return "", fmt.Errorf("제안 생성이 실패했습니다: %s", event.Message)
		}
	}
	output := strings.TrimSpace(builder.String())
	if output == "" {
		return "", ErrNoProposal
	}
	return output, nil
}

// repairPrompt sends the objections back with the original task.
//
// The objections come back in full rather than as "try again": a proposer told
// only that it failed will produce a variation of the same thing, and the
// round is spent either way.
func repairPrompt(req AuthorRequest, refusals []rrsi.Refusal) string {
	return prompt.Proposal(req.Direction, req.History, req.Configuration, req.CaseCount) +
		"\n\n직전 제안이 심사에서 거절되었다. 아래를 모두 고쳐서 다시 제시하라:\n" + rrsi.Explain(refusals)
}

// parseProposal reads the structured answer.
func parseProposal(raw string) ([]rrsi.Edit, error) {
	payload := extractJSON(raw)
	var decoded struct {
		Edits []struct {
			Component  string `json:"component"`
			Hypothesis string `json:"hypothesis"`
			Detail     string `json:"detail"`
			Change     *struct {
				Field string `json:"field"`
				To    string `json:"to"`
			} `json:"change"`
		} `json:"edits"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return nil, fmt.Errorf("구조화된 응답을 읽지 못했습니다: %w", err)
	}
	if len(decoded.Edits) == 0 {
		return nil, errors.New("편집이 하나도 없습니다")
	}
	edits := make([]rrsi.Edit, 0, len(decoded.Edits))
	for _, entry := range decoded.Edits {
		edit := rrsi.Edit{Component: strings.TrimSpace(entry.Component),
			Hypothesis: strings.TrimSpace(entry.Hypothesis), Detail: strings.TrimSpace(entry.Detail)}
		if entry.Change != nil {
			field, to := strings.TrimSpace(entry.Change.Field), strings.TrimSpace(entry.Change.To)
			// A change naming no field, or no value, is prose claiming to be
			// applicable. Dropping it keeps the edit as the suggestion it
			// actually is rather than failing the whole proposal.
			if field != "" && to != "" {
				edit.Change = &rrsi.Change{Field: field, To: to}
			}
		}
		edits = append(edits, edit)
	}
	return edits, nil
}

// extractJSON finds the object in an answer that may be wrapped in prose or a
// fenced block. A provider that was asked for JSON and returned it inside an
// explanation has answered; refusing that would spend a repair round on
// formatting.
func extractJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		return trimmed[start : end+1]
	}
	return trimmed
}
