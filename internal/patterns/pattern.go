// Package patterns keeps the fixes that worked, and the evidence that they
// did.
//
// A fix that solved a problem in one project is a fix. Calling it a pattern
// and recommending it everywhere is how one team's local quirk becomes a
// standard nobody chose — and the projects that adopt it inherit a constraint
// whose reason they cannot reconstruct.
package patterns

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// A pattern's standing.
const (
	// StatusCandidate means it has worked somewhere and is not yet a
	// recommendation.
	StatusCandidate = "CANDIDATE"
	// StatusApproved means a person reviewed it and put it forward.
	StatusApproved = "APPROVED"
	// StatusRetired means it stopped working often enough that recommending
	// it does more harm than the problem it addresses.
	StatusRetired = "RETIRED"
)

// Outcomes of applying a pattern.
const (
	OutcomePassed = "PASSED"
	OutcomeFailed = "FAILED"
)

// MinimumProjects is how many distinct projects a pattern must have worked in
// before it can be put forward.
//
// Two rather than one because one project's fix might be about that project.
// Two rather than five because the point is to catch the local quirk, not to
// make the archive unusable until a fix has spread on its own.
const MinimumProjects = 2

// Pattern is a fix that worked, with the conditions it worked under.
type Pattern struct {
	ID string `json:"id"`
	// StandardID is the criterion this addresses, so a project that fails that
	// criterion can be shown what has worked elsewhere.
	StandardID string `json:"standard_id"`
	// Problem is the recurring defect in the words someone hitting it would
	// use, and Approach is what fixed it.
	Problem  string `json:"problem"`
	Approach string `json:"approach"`
	// AppliesWhen narrows the pattern to projects it can work in. A fix that
	// depends on Postgres is not advice for a project without one.
	AppliesWhen map[string]string `json:"applies_when,omitempty"`
	Source      string            `json:"source,omitempty"`
	Status      string            `json:"status"`
	// Decider and DecidedAt record who put it forward. A recommendation nobody
	// owns is one nobody will withdraw.
	Decider   string    `json:"decider,omitempty"`
	DecidedAt time.Time `json:"decided_at,omitempty"`
	// RetiredReason says why it stopped being recommended, which is what stops
	// the next person re-proposing it.
	RetiredReason string `json:"retired_reason,omitempty"`
}

// Validate refuses a pattern that cannot be acted on.
func (p Pattern) Validate() error {
	switch {
	case strings.TrimSpace(p.ID) == "":
		return errors.New("패턴 ID가 필요합니다")
	case strings.TrimSpace(p.StandardID) == "":
		return fmt.Errorf("%s: 어떤 기준에 대한 것인지 적어야 합니다", p.ID)
	case strings.TrimSpace(p.Problem) == "":
		return fmt.Errorf("%s: 어떤 문제를 푸는지 적어야 합니다", p.ID)
	case strings.TrimSpace(p.Approach) == "":
		// Without it the archive holds a list of problems somebody once had,
		// which is not something a reader can do anything with.
		return fmt.Errorf("%s: 어떻게 풀었는지 적어야 합니다", p.ID)
	case p.Status == StatusApproved && strings.TrimSpace(p.Decider) == "":
		return fmt.Errorf("%s: 승인에는 결정자가 필요합니다 — 아무도 소유하지 않은 권고는 아무도 철회하지 않습니다", p.ID)
	case p.Status == StatusRetired && strings.TrimSpace(p.RetiredReason) == "":
		return fmt.Errorf("%s: 철회 사유가 필요합니다 — 없으면 다음 사람이 같은 것을 다시 제안합니다", p.ID)
	}
	return nil
}

// AppliesTo reports whether a project's declared attributes admit this
// pattern.
func (p Pattern) AppliesTo(attributes map[string]string) bool {
	for key, want := range p.AppliesWhen {
		if !strings.EqualFold(strings.TrimSpace(attributes[key]), strings.TrimSpace(want)) {
			return false
		}
	}
	return true
}

// Application is one time a pattern was used, and what happened.
type Application struct {
	PatternID, ProjectID, WorkItemID string
	CommitSHA, Outcome, Detail       string
	AppliedAt                        time.Time
}

// Evidence is what the applications say about a pattern.
type Evidence struct {
	// Projects is how many distinct projects it has worked in. It is projects
	// rather than applications because five successes in one project are five
	// pieces of evidence about that project.
	Projects int
	Passed   int
	Failed   int
	// RecentFailures counts failures since the last success, which is what
	// says a pattern has stopped working rather than that it once had a bad
	// day.
	RecentFailures int
	FailedIn       []string
}

// Promotable reports whether the evidence supports putting a pattern forward.
func (e Evidence) Promotable() bool {
	return e.Projects >= MinimumProjects && e.RecentFailures < RetireAfterFailures
}

// RetireAfterFailures is how many failures since the last success retire a
// pattern.
//
// A pattern that has failed three times running is not a pattern that had a
// bad day, and continuing to recommend it costs the next three projects the
// same time it cost the last three.
const RetireAfterFailures = 3

// ShouldRetire reports whether a pattern has stopped working.
func (e Evidence) ShouldRetire() bool { return e.RecentFailures >= RetireAfterFailures }

// Summarize reads a pattern's applications, newest last.
func Summarize(applications []Application) Evidence {
	sorted := append([]Application{}, applications...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].AppliedAt.Before(sorted[j].AppliedAt) })
	var evidence Evidence
	projects := map[string]bool{}
	for _, application := range sorted {
		switch application.Outcome {
		case OutcomePassed:
			evidence.Passed++
			projects[application.ProjectID] = true
			// A success ends the run of failures: whatever was wrong was
			// fixed, and counting the old failures against it forever would
			// retire a pattern that now works.
			evidence.RecentFailures = 0
		case OutcomeFailed:
			evidence.Failed++
			evidence.RecentFailures++
			evidence.FailedIn = append(evidence.FailedIn, application.ProjectID)
		}
	}
	evidence.Projects = len(projects)
	return evidence
}

// Reason explains a promotion decision in the terms the reader acts on.
func (e Evidence) Reason() string {
	switch {
	case e.ShouldRetire():
		return fmt.Sprintf("마지막 성공 이후 %d번 연속 실패했습니다 — 권고를 유지하면 다음 프로젝트들이 같은 시간을 씁니다",
			e.RecentFailures)
	case e.Projects == 0:
		return "아직 어디에서도 통하지 않았습니다"
	case e.Projects < MinimumProjects:
		return fmt.Sprintf("프로젝트 %d개에서만 통했습니다 — 한 곳에서 통한 것은 그 프로젝트에 관한 것일 수 있습니다",
			e.Projects)
	}
	return fmt.Sprintf("프로젝트 %d개에서 통했습니다 (성공 %d · 실패 %d)", e.Projects, e.Passed, e.Failed)
}
