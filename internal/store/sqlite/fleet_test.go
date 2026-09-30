package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/patterns"
	"github.com/goalforge/goalforge/internal/standards"
)

func fleetFixture(t *testing.T) (context.Context, *Store, standards.Pack) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return ctx, s, standards.GoReactOfflineService()
}

func fleetProject(t *testing.T, ctx context.Context, s *Store, pack standards.Pack, id string, exceptions ...standards.Exception) {
	t.Helper()
	if err := s.CreateProject(ctx, model.Project{ID: id, Name: id, RepositoryPath: "/repo/" + id,
		DefaultBranch: "main", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetGoal(ctx, id, "G", "o", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}}); err != nil {
		t.Fatal(err)
	}
	profile := standards.Profile{ProjectID: id, PackRef: pack.Ref(),
		Attributes: map[string]string{"frontend": "react", "network": "offline"}, Exceptions: exceptions}
	if err := s.SaveStandardProfile(ctx, profile, pack); err != nil {
		t.Fatal(err)
	}
}

func waiver(standardID string) standards.Exception {
	return standards.Exception{StandardID: standardID, Reason: "우리 제품에는 해당하지 않습니다",
		Decider: "hkjang", ReviewWhen: "요건이 바뀌면"}
}

// A criterion most of a fleet has excused is not one most of a fleet is
// failing — it is one that does not fit the work these projects do. Leaving it
// in means every new project inherits an exception to write, and the
// exceptions become a ritual rather than a decision.
func TestACriterionEveryoneExcusesIsEvidenceAgainstTheCriterion(t *testing.T) {
	ctx, s, pack := fleetFixture(t)
	for _, id := range []string{"PRJ-1", "PRJ-2", "PRJ-3"} {
		fleetProject(t, ctx, s, pack, id, waiver("UX-005"))
	}
	fleetProject(t, ctx, s, pack, "PRJ-4")
	proposals, err := s.ProposePackChanges(ctx, pack, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := ""
	for _, proposal := range proposals {
		if proposal.StandardID == "UX-005" {
			found = proposal.Evidence
		}
	}
	if found == "" {
		t.Fatalf("three of four excusing it is a signal: %+v", proposals)
	}
	if !strings.Contains(found, "PRJ-1") || !strings.Contains(found, "4") {
		t.Fatalf("the evidence must name who and how many: %q", found)
	}
}

// One project cannot show that a criterion is wrong, only that it did not suit
// one project.
func TestOneProjectsExceptionIsNotEvidenceAgainstACriterion(t *testing.T) {
	ctx, s, pack := fleetFixture(t)
	fleetProject(t, ctx, s, pack, "PRJ-1", waiver("UX-005"))
	proposals, err := s.ProposePackChanges(ctx, pack, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, proposal := range proposals {
		if proposal.StandardID == "UX-005" {
			t.Fatalf("one project is not a fleet: %+v", proposal)
		}
	}
	// And a minority across a real fleet is still that fleet's circumstance,
	// not the criterion's fault. Raising a proposal at the first exception
	// would make the catalogue churn on every project's local decision.
	fleetProject(t, ctx, s, pack, "PRJ-2")
	fleetProject(t, ctx, s, pack, "PRJ-3")
	if proposals, err = s.ProposePackChanges(ctx, pack, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, proposal := range proposals {
		if proposal.StandardID == "UX-005" {
			t.Fatalf("one of three excusing it is not evidence against it: %+v", proposal)
		}
	}
}

// A required criterion nothing can judge blocks everyone without ever having
// been passed.
func TestARequiredCriterionNobodyCanJudgeIsProposedForChange(t *testing.T) {
	ctx, s, pack := fleetFixture(t)
	fleetProject(t, ctx, s, pack, "PRJ-1")
	fleetProject(t, ctx, s, pack, "PRJ-2")
	// A fleet nobody has assessed has every required criterion at UNKNOWN.
	// Flagging all of them would say the catalogue is wrong when what is true
	// is that nobody has run it yet, and the suggested fix — attach a gate —
	// is not a change to the catalogue at all.
	fresh, err := s.ProposePackChanges(ctx, pack, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, proposal := range fresh {
		if strings.Contains(proposal.Evidence, "미확인") {
			t.Fatalf("a fleet that has never been assessed proves nothing about the catalogue: %+v", proposal)
		}
	}
	// Once the fleet demonstrably settles some criteria, one that stays
	// unjudgeable everywhere stands out.
	settled, _ := pack.Standard("NET-002")
	if _, err = s.RecordAssessment(ctx, settled, standards.Assessment{ProjectID: "PRJ-1",
		StandardID: "NET-002", CommitSHA: "abc123", Result: standards.ResultMet,
		Evidence: []standards.Evidence{
			{Kind: "commit_sha", Detail: "abc123", Observed: true},
			{Kind: "browser_test_result", Detail: "통과", Observed: true},
		}}, false); err != nil {
		t.Fatal(err)
	}
	proposals, err := s.ProposePackChanges(ctx, pack, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, proposal := range proposals {
		if proposal.StandardID == "CFG-002" {
			found = true
			if !strings.Contains(proposal.Change, "게이트") {
				t.Fatalf("the change must say what would fix it: %q", proposal.Change)
			}
		}
	}
	if !found {
		t.Fatalf("a required criterion at UNKNOWN everywhere is worth raising: %+v", proposals)
	}
	// Once one project settles it, it is no longer nobody-can-judge.
	standard, _ := pack.Standard("CFG-002")
	if _, err = s.RecordAssessment(ctx, standard, standards.Assessment{ProjectID: "PRJ-1",
		StandardID: "CFG-002", CommitSHA: "abc123", Result: standards.ResultUnmet}, false); err != nil {
		t.Fatal(err)
	}
	if proposals, err = s.ProposePackChanges(ctx, pack, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, proposal := range proposals {
		if proposal.StandardID == "CFG-002" {
			t.Fatal("somebody judged it, so the criterion is not the problem")
		}
	}
}

// Projects that never pinned this pack are not part of its fleet. Counting
// them would dilute every number with projects that never agreed to be
// measured.
func TestProjectsOutsideThePackAreNotCounted(t *testing.T) {
	ctx, s, pack := fleetFixture(t)
	fleetProject(t, ctx, s, pack, "PRJ-1")
	if err := s.CreateProject(ctx, model.Project{ID: "OTHER", Name: "OTHER", RepositoryPath: "/r",
		DefaultBranch: "main", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	// A project pinned to a different version of the same pack is held to a
	// different catalogue. Counting it here would measure it against criteria
	// it never agreed to — which is the whole reason versions are pinned.
	newer := pack
	newer.Version = "0.2"
	if err := s.SaveStandardProfile(ctx, standards.Profile{ProjectID: "OTHER", PackRef: newer.Ref(),
		Attributes: map[string]string{"frontend": "react"}}, newer); err != nil {
		t.Fatal(err)
	}
	report, err := s.Fleet(ctx, pack, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Projects) != 1 || report.Projects[0] != "PRJ-1" {
		t.Fatalf("projects=%v", report.Projects)
	}
	for _, criterion := range report.Criteria {
		if criterion.Applicable != 1 {
			t.Fatalf("%s applicable=%d", criterion.StandardID, criterion.Applicable)
		}
	}
}

// A criterion out of profile somewhere is not a gap there. Folding those in
// would make every conditional criterion look half-met forever.
func TestCriteriaOutsideAProjectsProfileAreNotCountedAgainstIt(t *testing.T) {
	ctx, s, pack := fleetFixture(t)
	fleetProject(t, ctx, s, pack, "PRJ-1")
	report, err := s.Fleet(ctx, pack, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, criterion := range report.Criteria {
		if strings.HasPrefix(criterion.StandardID, "AI-") {
			t.Fatalf("this fleet has no AI project: %+v", criterion)
		}
	}
	if len(report.Criteria) == 0 {
		t.Fatal("the criteria that do apply are still reported")
	}
}

// The archive keeps what happened, and the store hands it back in the order it
// happened.
func TestPatternApplicationsComeBackInOrder(t *testing.T) {
	ctx, s, _ := fleetFixture(t)
	if err := s.SavePattern(ctx, patterns.Pattern{ID: "PAT-1", StandardID: "NET-002",
		Problem: "번들에 CDN 참조가 남습니다", Approach: "외부 폰트를 로컬로 내립니다",
		Status: patterns.StatusCandidate}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i, entry := range []struct {
		project, outcome string
		day              int
	}{
		{"PRJ-2", patterns.OutcomeFailed, 3},
		{"PRJ-1", patterns.OutcomePassed, 1},
		{"PRJ-2", patterns.OutcomePassed, 5},
	} {
		_ = i
		if err := s.RecordPatternApplication(ctx, patterns.Application{PatternID: "PAT-1",
			ProjectID: entry.project, Outcome: entry.outcome,
			AppliedAt: base.AddDate(0, 0, entry.day)}); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := s.PatternApplications(ctx, "PAT-1")
	if err != nil {
		t.Fatal(err)
	}
	// The store's contract is oldest first, because a run of failures ending
	// in a success means something different from a success followed by
	// failures, and a caller reading the list in order must see the former.
	for i := 1; i < len(stored); i++ {
		if stored[i].AppliedAt.Before(stored[i-1].AppliedAt) {
			t.Fatalf("applications must come back oldest first: %v", stored)
		}
	}
	view, err := s.PatternByID(ctx, "PAT-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Evidence.Projects != 2 || view.Evidence.RecentFailures != 0 {
		t.Fatalf("evidence=%+v", view.Evidence)
	}
	if !view.Evidence.Promotable() {
		t.Fatalf("two projects and no recent failures: %+v", view.Evidence)
	}
}

// An application with no outcome is a note that somebody tried something, and
// the archive's whole job is to say whether trying it worked.
func TestAnApplicationWithoutAnOutcomeIsRefused(t *testing.T) {
	ctx, s, _ := fleetFixture(t)
	if err := s.SavePattern(ctx, patterns.Pattern{ID: "PAT-1", StandardID: "NET-002",
		Problem: "p", Approach: "a", Status: patterns.StatusCandidate}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPatternApplication(ctx, patterns.Application{PatternID: "PAT-1",
		ProjectID: "PRJ-1", Outcome: "TRIED"}); err == nil {
		t.Fatal("an application must say whether it worked")
	}
}

// An invalid pattern does not reach the database.
func TestAnUnusablePatternIsNotStored(t *testing.T) {
	ctx, s, _ := fleetFixture(t)
	if err := s.SavePattern(ctx, patterns.Pattern{ID: "PAT-1", StandardID: "NET-002",
		Problem: "p", Status: patterns.StatusCandidate}); err == nil {
		t.Fatal("a pattern with no fix must be refused")
	}
	if _, err := s.PatternByID(ctx, "PAT-1"); err != ErrNotFound {
		t.Fatalf("nothing must have been written: %v", err)
	}
}
