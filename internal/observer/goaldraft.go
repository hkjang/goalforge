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
	// ValuePattern extracts the measurement from the gate's output, and for a
	// test runner it is what proves the test ran at all.
	//
	// `go test -run ^TestX$ ./pkg` exits zero when the package has no test
	// file, so a criterion settled by that gate went green the moment an empty
	// package existed. A pattern naming the test turns that vacuous pass into
	// a failure, because the output of a run with nothing to run does not
	// contain it.
	ValuePattern  string `json:"value_pattern,omitempty"`
	WhyItFailsNow string `json:"why_it_fails_now"`
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
	// Health is the one gate that must keep passing after every run.
	//
	// It is separate from the criteria because the two answer different
	// questions, and the rest of the system already keeps them apart:
	// verification gates run after every work item and decide whether that run
	// broke anything, while the goal's criteria decide whether the goal is
	// done. Installing completion criteria as required gates gave a goal with
	// six features six gates no single work item could satisfy — the first
	// item implements one piece and the end-to-end gate still fails because
	// nothing else exists yet. Every run failed, the repair loop burned its
	// attempts, and a multi-part goal could never make progress.
	Health   *DraftCriterion `json:"health,omitempty"`
	Refusals []rrsi.Refusal  `json:"refusals,omitempty"`
	Attempts int             `json:"attempts"`
}

// Gates is the draft as verification gates.
//
// Only the health gate is required. A completion criterion is measured — its
// result is what the goal's progress is read from — but it must not fail a run
// that has not reached it yet.
func (d GoalDraft) Gates() []store.GateConfig {
	gates := make([]store.GateConfig, 0, len(d.Criteria)+1)
	if d.Health != nil {
		gates = append(gates, store.GateConfig{Type: d.Health.Type, Command: d.Health.GateCommand,
			Timeout: goalGateTimeout, Required: true, SuccessValue: d.Health.ExpectedValue,
			Kind: d.Health.Kind})
	}
	for _, criterion := range d.Criteria {
		gates = append(gates, store.GateConfig{Type: criterion.Type, Command: criterion.GateCommand,
			Timeout: goalGateTimeout, Required: false, SuccessValue: criterion.ExpectedValue,
			// Carried through, because it is what makes a test gate fail when
			// the test does not exist. Dropping it here would reinstate the
			// vacuous pass with the screen still reporting the gate as checked.
			ValuePattern: criterion.ValuePattern, Kind: criterion.Kind})
	}
	return gates
}

// goalGateTimeout bounds a gate once it is installed. Longer than the trial
// timeout: the trial only asks whether the command runs and fails, while the
// installed gate has to let a real suite finish.
const goalGateTimeout = 15 * time.Minute

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
	// RefusalNoHealthGate means nothing would fail a run.
	//
	// The runner refuses a project whose gates are all optional, so a draft
	// without one would set the goal and leave nothing able to run — worse
	// than refusing it, because the refusal says what is missing and the other
	// leaves a project that looks ready.
	RefusalNoHealthGate = "NO_HEALTH_GATE"
	// RefusalVacuousPass means the gate can pass without the thing it measures
	// existing.
	//
	// A filtered test run is the case that matters: go test -run ^TestX$ ./pkg
	// exits zero when the package has no test file, so the criterion goes
	// green the moment an empty package exists and the goal is reported
	// complete for a feature nobody implemented. "Fails now" does not catch it
	// — at draft time the package is missing, so the gate does fail, for a
	// reason that stops applying as soon as any file is written.
	RefusalVacuousPass = "VACUOUS_PASS"
	// RefusalHealthGateFails means the proposed health gate already fails. One
	// that fails today is not a health gate; it is a completion criterion
	// under the wrong heading, and making it required would fail every run
	// until the whole goal was done.
	RefusalHealthGateFails = "HEALTH_GATE_FAILS"
)

// testRunners are commands that exit zero when their filter matches nothing,
// keyed by the subcommand that has to be present for it to be a test run.
//
// This is language-specific knowledge and is written down as such, the same way
// the gate templates know what a Go or Node project is built with. `go test`
// prints "[no test files]" and exits zero; a filtered run that matched nothing
// prints "no tests to run" and still passes. A gate built on one of these
// without an assertion that the named test ran is satisfied by an empty
// package.
//
// The subcommand matters: `go build ./...` cannot pass vacuously, and keying on
// the executable alone demanded a pattern from every Go command.
var testRunners = map[string]string{
	"go": "test", "pytest": "", "vitest": "", "jest": "", "npx": "vitest",
}

// isTestRunner reports whether a command is one of the runners that passes on
// an empty filter result.
func isTestRunner(command []string) bool {
	if len(command) == 0 {
		return false
	}
	subcommand, known := testRunners[baseCommand(command[0])]
	if !known {
		return false
	}
	if subcommand == "" {
		return true
	}
	for _, arg := range command[1:] {
		if arg == subcommand {
			return true
		}
		if !strings.HasPrefix(arg, "-") {
			// The first non-flag argument is the subcommand. Anything else
			// there means this is not a test run.
			return false
		}
	}
	return false
}

// filterFlags are how each runner is told to run only some tests.
var filterFlags = map[string]bool{"-run": true, "-k": true, "-t": true, "--testNamePattern": true}

// vacuousPass reports why a gate could pass without the thing it measures, or
// "" when it could not.
//
// Only filtered test runs are held to this. A build command or a file check
// cannot pass vacuously in the same way, and demanding a pattern from them
// would make the drafter invent one.
func vacuousPass(criterion DraftCriterion) string {
	if !isTestRunner(criterion.GateCommand) {
		return ""
	}
	filter, verbose := "", false
	for i, arg := range criterion.GateCommand {
		switch {
		case filterFlags[arg] && i+1 < len(criterion.GateCommand):
			filter = criterion.GateCommand[i+1]
		case arg == "-v" || arg == "--verbose":
			verbose = true
		}
	}
	if filter == "" {
		// An unfiltered run still passes on a package with no tests, so it
		// needs the assertion just as much.
		if strings.TrimSpace(criterion.ValuePattern) == "" {
			return fmt.Sprintf("%s: 이 명령은 테스트가 하나도 없어도 통과합니다 (go test 는 [no test files] 로 exit 0) — 어떤 검사가 실제로 통과했는지 확인하는 value_pattern 이 필요합니다",
				criterion.Type)
		}
		return ""
	}
	if !verbose {
		return fmt.Sprintf("%s: 걸러낸 실행(%s %s)은 -v 없이는 어떤 검사가 통과했는지 출력하지 않습니다 — -v 와 value_pattern 을 함께 주세요",
			criterion.Type, "-run", filter)
	}
	pattern := strings.TrimSpace(criterion.ValuePattern)
	if pattern == "" {
		return fmt.Sprintf("%s: 이 명령은 %s 에 맞는 검사가 없어도 통과합니다 — 그 검사가 실제로 통과했음을 확인하는 value_pattern 이 필요합니다 (예: --- PASS: (TestRedirect))",
			criterion.Type, filter)
	}
	// The pattern has to name what the filter asked for. One that matches some
	// other line passes on the output of a run with nothing to run, with a
	// pattern attached to make it look checked.
	if !patternNames(pattern, filter) {
		return fmt.Sprintf("%s: value_pattern %q 이 걸러낸 이름(%s)을 담고 있지 않습니다 — 다른 줄에 맞으면 검사가 없어도 통과합니다",
			criterion.Type, pattern, filter)
	}
	return ""
}

// patternNames reports whether a value pattern mentions the test the filter
// selected, ignoring the anchors and escapes a filter usually carries.
func patternNames(pattern, filter string) bool {
	name := strings.Trim(filter, "^$'\"")
	name = strings.TrimPrefix(name, "\\b")
	name = strings.TrimSuffix(name, "\\b")
	if name == "" {
		return false
	}
	return strings.Contains(pattern, name)
}

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
	// Structural only: whether a health gate was proposed at all. Whether it
	// actually passes is VerifyDraftFailsNow's answer, because answering it
	// means running the command — and the same rule written in both places
	// would be two rules.
	switch {
	case draft.Health == nil:
		refusals = append(refusals, rrsi.Refusal{Kind: RefusalNoHealthGate,
			Detail: "매 실행 뒤 통과해야 할 게이트가 없습니다 — 완료 조건만으로는 부분 작업이 모두 실패합니다"})
	case len(draft.Health.GateCommand) == 0 || trivialCommands[baseCommand(draft.Health.GateCommand[0])]:
		// The same rule the criteria get, for the same reason. A health gate
		// that cannot fail is the one required gate catching nothing, and then
		// every run is "verified" by a command that was green before it ran.
		refusals = append(refusals, rrsi.Refusal{Kind: RefusalNoHealthGate,
			Detail: fmt.Sprintf("%s 는 항상 통과하는 명령입니다 — 아무것도 잡지 못하는 필수 게이트입니다",
				draft.Health.Type)})
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
		if detail := vacuousPass(criterion); detail != "" {
			refusals = append(refusals, rrsi.Refusal{Kind: RefusalVacuousPass, Detail: detail})
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
	// The health gate is run too, and the answer wanted is the opposite one.
	// It is checked here rather than trusted from the draft: a proposer that
	// says "go build ./... passes" about a repository that does not compile
	// would install a gate failing every run.
	if draft.Health != nil {
		results, _, err := engine.Check(ctx, repository, []verification.Gate{{
			Type: draft.Health.Type, Command: draft.Health.GateCommand, Required: true,
			SuccessValue: draft.Health.ExpectedValue, Kind: draft.Health.Kind,
			Timeout: draftGateTimeout}})
		switch {
		case err != nil || len(results) == 0 || results[0].ExitCode < 0:
			detail := "명령을 실행할 수 없었습니다"
			if err != nil {
				detail = err.Error()
			} else if len(results) > 0 && strings.TrimSpace(results[0].Output) != "" {
				detail = firstLineOf(results[0].Output)
			}
			// Marked as failing so the screen refuses it. A health gate nobody
			// could run is not one that passes.
			draft.Health.FailsNow = true
			refusals = append(refusals, rrsi.Refusal{Kind: RefusalHealthGateFails,
				Detail: fmt.Sprintf("%s: 게이트를 돌릴 수 없었습니다: %s", draft.Health.Type, detail)})
		default:
			draft.Health.FailsNow = results[0].Status != "PASSED"
			draft.Health.Output = results[0].Output
			if draft.Health.FailsNow {
				refusals = append(refusals, rrsi.Refusal{Kind: RefusalHealthGateFails,
					Detail: fmt.Sprintf("%s: 지금 이미 실패합니다 — 매 실행을 막는 게이트가 되므로 목표가 끝날 때까지 아무것도 진행되지 않습니다: %s",
						draft.Health.Type, firstLineOf(results[0].Output))})
			}
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
	// Qwen Code repeats the whole answer in its final result event, so a
	// completed event replaces the streamed messages instead of joining them.
	var builder strings.Builder
	var completed string
	for event := range events {
		switch event.Type {
		case provider.EventMessage:
			builder.WriteString(event.Message)
		case provider.EventCompleted:
			completed = event.Message
		case provider.EventFailed:
			return "", fmt.Errorf("초안 생성이 실패했습니다: %s", event.Message)
		}
	}
	output := strings.TrimSpace(completed)
	if output == "" {
		output = strings.TrimSpace(builder.String())
	}
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
