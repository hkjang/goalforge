package observer

import (
	"context"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/rrsi"
)

// scriptedProvider answers with a canned reply per attempt, and records what it
// was asked, so a test can check that the objections actually reached it.
type scriptedProvider struct {
	replies  []string
	prompts  []string
	failWith string
}

func (p *scriptedProvider) Name() string                        { return "scripted" }
func (p *scriptedProvider) Capabilities() provider.Capabilities { return provider.Capabilities{} }
func (p *scriptedProvider) Start(_ context.Context, request provider.RunRequest) (<-chan provider.Event, error) {
	p.prompts = append(p.prompts, request.Prompt)
	index := len(p.prompts) - 1
	events := make(chan provider.Event, 2)
	go func() {
		defer close(events)
		if p.failWith != "" {
			events <- provider.Event{Type: provider.EventFailed, Message: p.failWith}
			return
		}
		reply := ""
		if index < len(p.replies) {
			reply = p.replies[index]
		} else if len(p.replies) > 0 {
			reply = p.replies[len(p.replies)-1]
		}
		events <- provider.Event{Type: provider.EventMessage, Message: reply}
	}()
	return events, nil
}
func (p *scriptedProvider) Resume(context.Context, string, provider.RunRequest) (<-chan provider.Event, error) {
	return nil, nil
}
func (p *scriptedProvider) GetQuota(context.Context, provider.AccountRef) (provider.QuotaSnapshot, error) {
	return provider.QuotaSnapshot{}, nil
}
func (p *scriptedProvider) Interrupt(context.Context, string) error { return nil }

func authorRequest(history rrsi.History, cases ...string) AuthorRequest {
	return AuthorRequest{ProjectID: "PRJ-1",
		Direction:     rrsi.Next(history, nil, 1, 10, 1, 4, 3, 0.02),
		History:       history,
		Configuration: "model=sonnet wip=1",
		CaseNames:     cases, CaseCount: len(cases)}
}

const goodProposal = `{"edits":[{"component":"context","hypothesis":"앞선 실행 요약을 주면 범위를 덜 벗어난다","detail":"실행 프롬프트에 직전 실행 요약 추가"}]}`

// A proposer is a model, so what it writes is the product of what it is shown.
// The screen is what enforces the rules the context asks for, because asking is
// not enforcing.
func TestAnUnscreenableProposalIsNotReturnedAsAccepted(t *testing.T) {
	leaky := `{"edits":[{"component":"prompt","hypothesis":"csv-export 사례를 특별히 다루면 통과한다","detail":"d"}]}`
	author := Author{Provider: &scriptedProvider{replies: []string{leaky}}, Repairs: 0}
	result, err := author.Write(context.Background(), authorRequest(nil, "csv-export", "login-flow"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted() {
		t.Fatal("a proposal naming an evaluation case must not come back accepted")
	}
	if result.Refusals[0].Kind != rrsi.RefusalLeakage {
		t.Fatalf("refusals=%+v", result.Refusals)
	}
}

// The objections come back in full. A proposer told only that it failed will
// produce a variation of the same thing, and the round is spent either way.
func TestARefusedProposalIsSentBackWithTheObjections(t *testing.T) {
	leaky := `{"edits":[{"component":"prompt","hypothesis":"csv-export 사례를 특별히 다루면 통과한다","detail":"d"}]}`
	scripted := &scriptedProvider{replies: []string{leaky, goodProposal}}
	author := Author{Provider: scripted, Repairs: 2}
	result, err := author.Write(context.Background(), authorRequest(nil, "csv-export"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted() {
		t.Fatalf("the repaired proposal passes: %+v", result.Refusals)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts=%d", result.Attempts)
	}
	if len(scripted.prompts) != 2 {
		t.Fatalf("prompts=%d", len(scripted.prompts))
	}
	if !strings.Contains(scripted.prompts[1], "LEAKAGE") {
		t.Fatalf("the second ask must carry the objection:\n%s", scripted.prompts[1])
	}
	if !strings.Contains(scripted.prompts[1], "csv-export") {
		t.Fatal("and name what leaked")
	}
}

// A proposer that cannot satisfy the screen in a few attempts is not going to
// on the tenth, and every attempt costs.
func TestRepairsAreBounded(t *testing.T) {
	leaky := `{"edits":[{"component":"prompt","hypothesis":"csv-export 를 특별히 다룬다","detail":"d"}]}`
	scripted := &scriptedProvider{replies: []string{leaky}}
	author := Author{Provider: scripted, Repairs: 2}
	result, err := author.Write(context.Background(), authorRequest(nil, "csv-export"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted() {
		t.Fatal("it never satisfied the screen")
	}
	if result.Attempts != 3 {
		t.Fatalf("one attempt plus two repairs: %d", result.Attempts)
	}
	if len(scripted.prompts) != 3 {
		t.Fatalf("prompts=%d", len(scripted.prompts))
	}
}

// A malformed answer is told what was wrong rather than asked again into the
// same silence.
func TestAMalformedAnswerIsRepairedLikeARefusal(t *testing.T) {
	scripted := &scriptedProvider{replies: []string{"죄송합니다. 무엇을 바꿔야 할지 모르겠습니다.", goodProposal}}
	author := Author{Provider: scripted, Repairs: 1}
	result, err := author.Write(context.Background(), authorRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted() {
		t.Fatalf("refusals=%+v", result.Refusals)
	}
	if !strings.Contains(scripted.prompts[1], "MALFORMED") {
		t.Fatalf("the second ask must say the answer was unreadable:\n%s", scripted.prompts[1])
	}
}

// A provider that returns the object inside an explanation has answered.
// Refusing that would spend a repair round on formatting.
func TestJSONInsideProseIsAccepted(t *testing.T) {
	wrapped := "다음을 제안합니다:\n```json\n" + goodProposal + "\n```\n이유는 위와 같습니다."
	author := Author{Provider: &scriptedProvider{replies: []string{wrapped}}}
	result, err := author.Write(context.Background(), authorRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted() || len(result.Edits) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if result.Edits[0].Component != "context" {
		t.Fatalf("edits=%+v", result.Edits)
	}
}

// A provider failure is an error, not an empty proposal. An empty proposal
// would be read as "nothing to change", which is a conclusion nobody reached.
func TestAProviderFailureIsNotAnEmptyProposal(t *testing.T) {
	author := Author{Provider: &scriptedProvider{failWith: "quota exhausted"}}
	result, err := author.Write(context.Background(), authorRequest(nil))
	if err == nil {
		t.Fatal("a provider failure must be an error")
	}
	if result.Accepted() {
		t.Fatal("and must not look like an accepted proposal")
	}
	if !strings.Contains(err.Error(), "quota") {
		t.Fatalf("the error must carry what the provider said: %v", err)
	}
}

// The screen's budget comes from the round, so a late-round proposal bundling
// four edits is refused even though the model was asked for at most four.
func TestTheRoundBudgetIsEnforcedOnWhatComesBack(t *testing.T) {
	four := `{"edits":[
		{"component":"prompt","hypothesis":"가설 하나","detail":"d"},
		{"component":"gate","hypothesis":"가설 둘","detail":"d"},
		{"component":"context","hypothesis":"가설 셋","detail":"d"},
		{"component":"scope","hypothesis":"가설 넷","detail":"d"}]}`
	request := authorRequest(nil)
	request.Direction.Budget = 1
	author := Author{Provider: &scriptedProvider{replies: []string{four}}}
	result, err := author.Write(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted() {
		t.Fatal("four edits on a budget of one must be refused")
	}
	found := false
	for _, refusal := range result.Refusals {
		if refusal.Kind == rrsi.RefusalBudget {
			found = true
		}
	}
	if !found {
		t.Fatalf("refusals=%+v", result.Refusals)
	}
}
