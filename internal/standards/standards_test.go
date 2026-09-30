package standards

import (
	"strings"
	"testing"
	"time"
)

func standard(id, severity string) Standard {
	return Standard{ID: id, Revision: 1, Title: id + " title", Category: "usability", Severity: severity,
		Intent: "do the thing", Checks: []Check{{Type: "browser_journey", Assertion: "it works"}},
		EvidenceRequired: []string{"commit_sha", "browser_test_result"}}
}

func pack(standards ...Standard) Pack {
	return Pack{ID: "go-react-offline-service", Version: "0.1", Title: "공통 개발팩", Standards: standards}
}

// The rule the whole catalogue rests on. A criterion nobody can settle is a
// sentence in a document: it can be quoted at people forever and never
// resolved, and a catalogue full of them looks like coverage while measuring
// nothing.
func TestACriterionThatCannotBeJudgedIsRefused(t *testing.T) {
	base := standard("UX-004", SeverityRequired)
	noChecks := base
	noChecks.Checks = nil
	if err := noChecks.Validate(); err == nil {
		t.Fatal("a criterion with no check must be refused")
	}
	noEvidence := base
	noEvidence.EvidenceRequired = nil
	if err := noEvidence.Validate(); err == nil {
		t.Fatal("a criterion that does not say what would settle it must be refused")
	}
	emptyAssertion := base
	emptyAssertion.Checks = []Check{{Type: "static"}}
	if err := emptyAssertion.Validate(); err == nil {
		t.Fatal("a check with no assertion cannot fail in a way anyone can read")
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Two criteria sharing an ID means an assessment cannot say which one it
// settled and an exception cannot say which one it excuses.
func TestDuplicateCriterionIDsAreRefused(t *testing.T) {
	if err := pack(standard("UX-004", SeverityRequired), standard("UX-004", SeverityRecommended)).Validate(); err == nil {
		t.Fatal("a pack with a repeated ID must be refused")
	}
}

// An exception without an owner is an omission wearing a decision's clothes.
func TestAnExceptionNeedsAReasonAndADecider(t *testing.T) {
	p := pack(standard("AUTH-002", SeverityRecommended))
	for _, exception := range []Exception{
		{StandardID: "AUTH-002", Decider: "hkjang"},
		{StandardID: "AUTH-002", Reason: "폐쇄망"},
	} {
		profile := Profile{ProjectID: "P-1", PackRef: p.Ref(), Exceptions: []Exception{exception}}
		if err := profile.Validate(p); err == nil {
			t.Fatalf("incomplete exception must be refused: %+v", exception)
		}
	}
	complete := Profile{ProjectID: "P-1", PackRef: p.Ref(), Exceptions: []Exception{
		{StandardID: "AUTH-002", Reason: "폐쇄망이라 외부 인증 제공자에 도달할 수 없습니다", Decider: "hkjang"}}}
	if err := complete.Validate(p); err != nil {
		t.Fatal(err)
	}
}

// A required criterion may be excepted, but not permanently by default. Without
// a review condition the exception outlives the circumstances that justified it
// and nobody notices.
func TestExceptingARequiredCriterionNeedsAReviewCondition(t *testing.T) {
	p := pack(standard("NET-001", SeverityRequired))
	open := Profile{ProjectID: "P-1", PackRef: p.Ref(), Exceptions: []Exception{
		{StandardID: "NET-001", Reason: "1단계에서는 범위 밖", Decider: "hkjang"}}}
	err := open.Validate(p)
	if err == nil {
		t.Fatal("an open-ended exception on a required criterion must be refused")
	}
	if !strings.Contains(err.Error(), "NET-001") {
		t.Fatalf("the refusal must name the criterion: %v", err)
	}
	dated := Profile{ProjectID: "P-1", PackRef: p.Ref(), Exceptions: []Exception{
		{StandardID: "NET-001", Reason: "1단계에서는 범위 밖", Decider: "hkjang",
			ReviewBy: time.Now().Add(720 * time.Hour)}}}
	if err := dated.Validate(p); err != nil {
		t.Fatal(err)
	}
	conditional := Profile{ProjectID: "P-1", PackRef: p.Ref(), Exceptions: []Exception{
		{StandardID: "NET-001", Reason: "1단계에서는 범위 밖", Decider: "hkjang",
			ReviewWhen: "폐쇄망 반입이 시작될 때"}}}
	if err := conditional.Validate(p); err != nil {
		t.Fatal(err)
	}
}

// An expired exception is not an exception. Treating it as one is how a
// six-week waiver becomes permanent.
func TestAnExpiredExceptionStopsExcusing(t *testing.T) {
	p := pack(standard("NET-001", SeverityRequired))
	profile := Profile{ProjectID: "P-1", PackRef: p.Ref(), Exceptions: []Exception{
		{StandardID: "NET-001", Reason: "임시", Decider: "hkjang", ReviewBy: time.Now().Add(-time.Hour)}}}
	if _, ok := profile.ExceptionFor("NET-001", time.Now()); ok {
		t.Fatal("an exception past its review date must stop excusing")
	}
	if len(InForce(p, profile, time.Now())) != 1 {
		t.Fatal("the criterion is back in force")
	}
}

// An exception naming a criterion the pack does not contain excuses nothing
// and hides a typo that looks like a decision.
func TestAnExceptionForAnUnknownCriterionIsRefused(t *testing.T) {
	p := pack(standard("NET-001", SeverityRequired))
	profile := Profile{ProjectID: "P-1", PackRef: p.Ref(), Exceptions: []Exception{
		{StandardID: "NET-011", Reason: "오타", Decider: "hkjang", ReviewWhen: "언젠가"}}}
	if err := profile.Validate(p); err == nil {
		t.Fatal("an exception for an unknown criterion must be refused")
	}
}

// applies_when is a fact about the project; an exception is a decision someone
// made. Reporting them the same way loses which of the two is being relied on.
func TestOutOfProfileIsNotTheSameAsExcepted(t *testing.T) {
	ai := standard("AI-001", SeverityRequired)
	ai.AppliesWhen = map[string]string{"ai": "true"}
	p := pack(ai, standard("UX-004", SeverityRequired))
	profile := Profile{ProjectID: "P-1", PackRef: p.Ref(), Attributes: map[string]string{"ai": "false"}}
	entries := Apply(p, profile, time.Now())
	if !entries[0].OutOfProfile || entries[0].Excepted {
		t.Fatalf("AI-001 does not apply to a project with no AI: %+v", entries[0])
	}
	if entries[1].OutOfProfile {
		t.Fatalf("UX-004 has no applies_when and applies everywhere: %+v", entries[1])
	}
	if len(InForce(p, profile, time.Now())) != 1 {
		t.Fatal("only UX-004 is in force")
	}
}

// The tempting failure is upward: a file exists, the README says so, the route
// is in the router — and the criterion is marked MET without anything having
// been run. UNMET files work that may be unnecessary; MET ships what nobody
// checked.
func TestAGuessCannotBecomeMet(t *testing.T) {
	s := standard("UX-004", SeverityRequired)
	partial := []Evidence{{Kind: "commit_sha", Detail: "abc123", Observed: true}}
	result, detail, err := Judge(s, ResultMet, partial, false)
	if err != nil {
		t.Fatal(err)
	}
	if result != ResultUnknown {
		t.Fatalf("missing evidence must not pass as met: %s", result)
	}
	if !strings.Contains(detail, "browser_test_result") || !strings.Contains(detail, "없습니다") {
		t.Fatalf("the detail must name what is missing: %q", detail)
	}
	// An entry with a kind and nothing in it is a ticked box, and a ticked box
	// marked "observed" is the easiest way to launder a guess into a pass.
	empty := []Evidence{
		{Kind: "commit_sha", Detail: "abc123", Observed: true},
		{Kind: "browser_test_result", Detail: "", Observed: true},
	}
	result, detail, err = Judge(s, ResultMet, empty, false)
	if err != nil {
		t.Fatal(err)
	}
	if result != ResultUnknown || !strings.Contains(detail, "없습니다") {
		t.Fatalf("evidence with no content is not evidence: %s %q", result, detail)
	}
	// All the required kinds are present, but one of them was concluded rather
	// than observed. That is an investigation, not a pass.
	inferred := []Evidence{
		{Kind: "commit_sha", Detail: "abc123", Observed: true},
		{Kind: "browser_test_result", Detail: "라우터 설정을 보니 동작할 것으로 보입니다", Observed: false},
	}
	result, detail, err = Judge(s, ResultMet, inferred, false)
	if err != nil {
		t.Fatal(err)
	}
	if result != ResultUnknown {
		t.Fatalf("an inference must not pass as met: %s", result)
	}
	if !strings.Contains(detail, "추정") || strings.Contains(detail, "없습니다") {
		t.Fatalf("the detail must say it was inferred: %q", detail)
	}
	observed := []Evidence{
		{Kind: "commit_sha", Detail: "abc123", Observed: true},
		{Kind: "browser_test_result", Detail: "journey.spec.ts 통과", Observed: true},
	}
	if result, _, err = Judge(s, ResultMet, observed, false); err != nil || result != ResultMet {
		t.Fatalf("observed evidence for every required kind is a pass: %s %v", result, err)
	}
}

// NOT_APPLICABLE is a decision, and the observer may not make it. Letting it
// would give the thing that measures the project the power to decide what the
// project is measured on.
func TestTheObserverCannotExcuseACriterionItself(t *testing.T) {
	s := standard("AUTH-002", SeverityRecommended)
	result, detail, err := Judge(s, ResultNotApplicable, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if result != ResultUnknown || detail == "" {
		t.Fatalf("result=%s detail=%q", result, detail)
	}
	if result, _, err = Judge(s, ResultNotApplicable, nil, true); err != nil || result != ResultNotApplicable {
		t.Fatalf("a recorded exception does excuse it: %s %v", result, err)
	}
}

// UNMET, PARTIAL and UNKNOWN pass through: only the upward move is guarded.
func TestTheOtherResultsPassThrough(t *testing.T) {
	s := standard("UX-004", SeverityRequired)
	for _, want := range []string{ResultUnmet, ResultPartial, ResultUnknown} {
		if result, _, err := Judge(s, want, nil, false); err != nil || result != want {
			t.Fatalf("%s: got %s %v", want, result, err)
		}
	}
	if _, _, err := Judge(s, "PASSED", nil, false); err == nil {
		t.Fatal("a result outside the five must be refused, not guessed at")
	}
}

// A new pack version proposes a difference; it does not re-judge a project
// that pinned the old one. A catalogue that changes underneath a project turns
// yesterday's finished work into today's finding with nothing having changed.
func TestANewPackVersionIsADifferenceNotAVerdict(t *testing.T) {
	v1 := pack(standard("UX-004", SeverityRequired), standard("NET-001", SeverityRequired))
	revised := standard("UX-004", SeverityRequired)
	revised.Revision = 2
	v2 := Pack{ID: v1.ID, Version: "0.2", Title: v1.Title,
		Standards: []Standard{revised, standard("KEY-001", SeverityRecommended)}}
	diff := DiffPacks(v1, v2)
	if len(diff.Added) != 1 || diff.Added[0] != "KEY-001" {
		t.Fatalf("added=%v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0] != "NET-001" {
		t.Fatalf("removed=%v", diff.Removed)
	}
	if len(diff.Revised) != 1 || diff.Revised[0] != "UX-004" {
		t.Fatalf("revised=%v", diff.Revised)
	}
	// A profile pinned to 0.1 cannot be validated against 0.2 by accident.
	profile := Profile{ProjectID: "P-1", PackRef: v1.Ref()}
	if err := profile.Validate(v2); err == nil {
		t.Fatal("a profile pinned to one version must not validate against another")
	}
	if DiffPacks(v1, v1).Empty() != true {
		t.Fatal("a version compared with itself asks for the same things")
	}
}

// The dedup key is what stops the same defect arriving as a new sentence every
// cycle. It must not move when the title does, and must differ when the defect
// or its target does.
func TestTheDedupKeyTracksTheDefectNotTheWording(t *testing.T) {
	base := DedupKey("P-1", "UX-004", "missing_route_restore", "web/src/routes")
	if base != DedupKey("p-1", " ux-004 ", "MISSING_ROUTE_RESTORE", "web/src/routes") {
		t.Fatal("the key must not move on case or surrounding space")
	}
	for _, other := range []string{
		DedupKey("P-2", "UX-004", "missing_route_restore", "web/src/routes"),
		DedupKey("P-1", "UX-005", "missing_route_restore", "web/src/routes"),
		DedupKey("P-1", "UX-004", "missing_filter_restore", "web/src/routes"),
		DedupKey("P-1", "UX-004", "missing_route_restore", "internal/http"),
	} {
		if other == base {
			t.Fatal("a different project, criterion, defect or target is a different finding")
		}
	}
}

// The checksum tells a project whether the catalogue it pinned is the one it is
// being judged by.
func TestTheChecksumMovesWithTheContent(t *testing.T) {
	v1 := pack(standard("UX-004", SeverityRequired))
	same := pack(standard("UX-004", SeverityRequired))
	if v1.Checksum() != same.Checksum() {
		t.Fatal("identical content has an identical checksum")
	}
	changed := standard("UX-004", SeverityRequired)
	changed.Intent = "something else"
	if v1.Checksum() == pack(changed).Checksum() {
		t.Fatal("changed content must change the checksum")
	}
}

// The shipped pack has to satisfy the rules it imposes. A catalogue that would
// be refused if someone submitted it is a catalogue nobody checked.
func TestTheShippedPackValidates(t *testing.T) {
	p := GoReactOfflineService()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(p.Standards) < 40 {
		t.Fatalf("the pack lost criteria: %d", len(p.Standards))
	}
	// Every criterion that applies only conditionally must say so through
	// applies_when rather than through a comment, or a project with no AI
	// reports a gap it does not have.
	for _, id := range []string{"AI-001", "AI-002", "AI-003", "NET-001", "NET-002", "AUTH-002", "KEY-001"} {
		standard, ok := p.Standard(id)
		if !ok {
			t.Fatalf("%s is missing from the pack", id)
		}
		if len(standard.AppliesWhen) == 0 {
			t.Fatalf("%s applies conditionally and must say when", id)
		}
	}
	// And UX-004, the example the specification works through, must be in force
	// for a plain React project.
	profile := Profile{ProjectID: "P-1", PackRef: p.Ref(),
		Attributes: map[string]string{"frontend": "react", "network": "offline"}}
	if err := profile.Validate(p); err != nil {
		t.Fatal(err)
	}
	inForce := map[string]bool{}
	for _, standard := range InForce(p, profile, time.Now()) {
		inForce[standard.ID] = true
	}
	if !inForce["UX-004"] || !inForce["NET-001"] {
		t.Fatal("a React project on an isolated network is held to UX-004 and NET-001")
	}
	if inForce["AI-001"] {
		t.Fatal("a project with no AI is not failing the streaming criterion")
	}
}

// Every required criterion must name evidence that can only come from running
// something. A required criterion settled by reading a file is settled by
// opinion.
func TestRequiredCriteriaAskForExecutedEvidence(t *testing.T) {
	for _, standard := range GoReactOfflineService().Standards {
		if standard.Severity != SeverityRequired {
			continue
		}
		found := false
		for _, kind := range standard.EvidenceRequired {
			if ExecutedEvidence(kind) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s is required but asks for no executed evidence: %v", standard.ID, standard.EvidenceRequired)
		}
	}
	// The rule is structural, not a convention this test happens to hold: a
	// required criterion asking only for locators must be refused outright.
	locatorsOnly := standard("REL-009", SeverityRequired)
	locatorsOnly.EvidenceRequired = []string{"commit_sha", "route"}
	if err := locatorsOnly.Validate(); err == nil {
		t.Fatal("a required criterion settled by reading must be refused")
	}
	// A recommended one may rest on reading: it is a finding, not a gate.
	locatorsOnly.Severity = SeverityRecommended
	if err := locatorsOnly.Validate(); err != nil {
		t.Fatal(err)
	}
}

// A card generated from a criterion quotes its intent to explain why the work
// exists. Without one the card says "NET-002 미충족" and sends whoever picks
// it up back to the catalogue.
func TestEveryShippedCriterionSaysWhatItIsFor(t *testing.T) {
	for _, standard := range GoReactOfflineService().Standards {
		if strings.TrimSpace(standard.Intent) == "" {
			t.Fatalf("%s has no intent", standard.ID)
		}
		if standard.Intent == standard.Title {
			t.Fatalf("%s: the intent must say what the criterion is for, not repeat its name", standard.ID)
		}
	}
	noIntent := standard("XX-001", SeverityRecommended)
	noIntent.Intent = ""
	if err := noIntent.Validate(); err == nil {
		t.Fatal("a criterion with no intent must be refused")
	}
}

// A criterion that presumes the project is a particular shape has to say so.
// Running the pack against GoalForge itself — a command-line tool that ships
// binaries — reported that it was missing a React dependency, a Dockerfile and
// a four-variable runtime contract. None of those is a defect in a CLI; all
// three were criteria describing a deployed web service and not saying so.
func TestCriteriaPresumingAWebServiceSayItInTheirConditions(t *testing.T) {
	pack := GoReactOfflineService()
	// A Go command-line tool: no web front end, not a deployed service.
	cli := Profile{ProjectID: "P-CLI", PackRef: pack.Ref(),
		Attributes: map[string]string{"frontend": "none", "deployment": "cli"}}
	if err := cli.Validate(pack); err != nil {
		t.Fatal(err)
	}
	inForce := map[string]bool{}
	for _, standard := range InForce(pack, cli, time.Now()) {
		inForce[standard.ID] = true
	}
	for _, id := range []string{"CORE-001", "CFG-001", "REL-001", "UX-001", "QA-001", "DOC-004"} {
		if inForce[id] {
			t.Fatalf("%s describes a deployed web service and must not apply to a CLI", id)
		}
	}
	// The criteria that genuinely apply to anything published still do.
	for _, id := range []string{"DOC-003"} {
		if !inForce[id] {
			t.Fatalf("%s applies to anything with users", id)
		}
	}
	// And the full-shape project is still held to all of them.
	service := Profile{ProjectID: "P-SVC", PackRef: pack.Ref(),
		Attributes: map[string]string{"frontend": "react", "deployment": "service", "network": "offline"}}
	held := map[string]bool{}
	for _, standard := range InForce(pack, service, time.Now()) {
		held[standard.ID] = true
	}
	for _, id := range []string{"CORE-001", "CFG-001", "REL-001", "UX-001", "QA-001", "DOC-004", "NET-001"} {
		if !held[id] {
			t.Fatalf("%s must apply to the project the pack is named after", id)
		}
	}
}

// A required criterion with no condition claims to apply to every project that
// pins this pack. The pack is named after one shape of project, so that claim
// has to be true of anything — and almost none of these are.
func TestRequiredCriteriaWithNoConditionApplyToAnyProject(t *testing.T) {
	universal := map[string]bool{
		// The only required criteria that hold whatever the project is.
		"NET-001": true, "NET-002": true, "AUTH-002": true, "AUTH-003": true, "AUTH-005": true,
		"AI-001": true, "AI-002": true, "AI-003": true, "KEY-001": true, "KEY-002": true,
		"INT-002": true, "INT-003": true, "WF-001": true, "WF-002": true,
	}
	for _, standard := range GoReactOfflineService().Standards {
		if standard.Severity != SeverityRequired || len(standard.AppliesWhen) > 0 {
			continue
		}
		if !universal[standard.ID] {
			t.Fatalf("%s (%s) is required with no condition — say which projects it is about",
				standard.ID, standard.Title)
		}
	}
}
