package patterns

import (
	"strings"
	"testing"
	"time"
)

func at(day int) time.Time { return time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC) }

func passed(project string, day int) Application {
	return Application{PatternID: "P-1", ProjectID: project, Outcome: OutcomePassed, AppliedAt: at(day)}
}

func failed(project string, day int) Application {
	return Application{PatternID: "P-1", ProjectID: project, Outcome: OutcomeFailed, AppliedAt: at(day)}
}

// The rule the archive exists for. A fix that solved a problem in one project
// is a fix; calling it a pattern and recommending it everywhere is how one
// team's local quirk becomes a standard nobody chose.
func TestOneProjectIsNotAPattern(t *testing.T) {
	single := Summarize([]Application{passed("PRJ-1", 1), passed("PRJ-1", 2), passed("PRJ-1", 3)})
	if single.Promotable() {
		t.Fatal("three successes in one project are three pieces of evidence about that project")
	}
	if single.Projects != 1 || single.Passed != 3 {
		t.Fatalf("evidence=%+v", single)
	}
	if !strings.Contains(single.Reason(), "그 프로젝트에 관한 것") {
		t.Fatalf("reason=%q", single.Reason())
	}
	second := Summarize([]Application{passed("PRJ-1", 1), passed("PRJ-2", 2)})
	if !second.Promotable() {
		t.Fatalf("two projects is the bar: %+v", second)
	}
}

// A pattern that has failed three times running is not one that had a bad day.
// Continuing to recommend it costs the next three projects what it cost the
// last three.
func TestAPatternThatStoppedWorkingIsRetired(t *testing.T) {
	evidence := Summarize([]Application{
		passed("PRJ-1", 1), passed("PRJ-2", 2),
		failed("PRJ-3", 3), failed("PRJ-4", 4), failed("PRJ-5", 5),
	})
	if !evidence.ShouldRetire() {
		t.Fatalf("evidence=%+v", evidence)
	}
	if evidence.Promotable() {
		t.Fatal("a retired pattern is not promotable however many projects it once worked in")
	}
	if !strings.Contains(evidence.Reason(), "연속 실패") {
		t.Fatalf("reason=%q", evidence.Reason())
	}
	if len(evidence.FailedIn) != 3 {
		t.Fatalf("the projects it failed in are what someone would go and look at: %v", evidence.FailedIn)
	}
}

// A success ends the run of failures. Counting old failures against a pattern
// forever would retire one that now works — and the person who fixed it would
// watch their fix stay marked broken.
func TestASuccessClearsTheFailureRun(t *testing.T) {
	evidence := Summarize([]Application{
		passed("PRJ-1", 1), passed("PRJ-2", 2),
		failed("PRJ-3", 3), failed("PRJ-4", 4),
		passed("PRJ-3", 5),
	})
	if evidence.ShouldRetire() {
		t.Fatalf("whatever was wrong was fixed: %+v", evidence)
	}
	if evidence.RecentFailures != 0 {
		t.Fatalf("recent=%d", evidence.RecentFailures)
	}
	if evidence.Failed != 2 {
		t.Fatalf("the failures still happened and are still counted: %+v", evidence)
	}
	if !evidence.Promotable() {
		t.Fatalf("evidence=%+v", evidence)
	}
}

// Applications are summarized in the order they happened, not the order they
// arrive. A store that returned them newest-first would make a pattern's
// history read backwards and its failure run start at the wrong end.
func TestTheOrderOfEventsIsWhatCounts(t *testing.T) {
	forwards := Summarize([]Application{failed("PRJ-1", 1), failed("PRJ-2", 2), passed("PRJ-3", 3)})
	backwards := Summarize([]Application{passed("PRJ-3", 3), failed("PRJ-2", 2), failed("PRJ-1", 1)})
	if forwards.RecentFailures != 0 || backwards.RecentFailures != 0 {
		t.Fatalf("the last thing that happened was a success: %+v %+v", forwards, backwards)
	}
}

// A pattern nobody can act on is a list of problems somebody once had.
func TestAPatternMustSayHowItWasFixed(t *testing.T) {
	base := Pattern{ID: "PAT-1", StandardID: "NET-002", Problem: "번들에 CDN 참조가 남습니다",
		Approach: "vite 설정에서 외부 폰트를 로컬로 내립니다", Status: StatusCandidate}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	noApproach := base
	noApproach.Approach = ""
	if err := noApproach.Validate(); err == nil {
		t.Fatal("a problem with no fix is not a pattern")
	}
	noProblem := base
	noProblem.Problem = ""
	if err := noProblem.Validate(); err == nil {
		t.Fatal("a fix with no problem cannot be matched to anything")
	}
	noStandard := base
	noStandard.StandardID = ""
	if err := noStandard.Validate(); err == nil {
		t.Fatal("a pattern must say which criterion it addresses")
	}
}

// A recommendation nobody owns is one nobody will withdraw, and a retirement
// with no reason is one the next person re-proposes.
func TestApprovalNeedsAnOwnerAndRetirementNeedsAReason(t *testing.T) {
	approved := Pattern{ID: "PAT-1", StandardID: "NET-002", Problem: "p", Approach: "a",
		Status: StatusApproved}
	if err := approved.Validate(); err == nil {
		t.Fatal("an approval with no decider must be refused")
	}
	approved.Decider = "hkjang"
	if err := approved.Validate(); err != nil {
		t.Fatal(err)
	}
	retired := Pattern{ID: "PAT-1", StandardID: "NET-002", Problem: "p", Approach: "a",
		Status: StatusRetired}
	if err := retired.Validate(); err == nil {
		t.Fatal("a retirement with no reason must be refused")
	}
}

// A fix that depends on something a project does not have is not advice for
// that project.
func TestAPatternOnlyAppliesWhereItCanWork(t *testing.T) {
	pattern := Pattern{ID: "PAT-1", StandardID: "AI-001", Problem: "p", Approach: "a",
		AppliesWhen: map[string]string{"ai": "true"}, Status: StatusApproved, Decider: "hkjang"}
	if pattern.AppliesTo(map[string]string{"ai": "false"}) {
		t.Fatal("a project with no AI cannot use an AI streaming fix")
	}
	if !pattern.AppliesTo(map[string]string{"ai": "true", "frontend": "react"}) {
		t.Fatal("extra attributes do not exclude it")
	}
	unconditional := pattern
	unconditional.AppliesWhen = nil
	if !unconditional.AppliesTo(nil) {
		t.Fatal("a pattern with no condition applies everywhere")
	}
}
