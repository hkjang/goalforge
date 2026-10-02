package sqlite

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/standards"
)

// packWith builds two versions of a one-criterion pack, the second having
// revised that criterion.
func packWith(revision int) standards.Pack {
	return standards.Pack{ID: "test", Version: "1", Title: "t",
		Standards: []standards.Standard{{ID: "T-001", Revision: revision, Title: "기준",
			Category: "quality", Severity: "required",
			Intent: "무언가를 보장한다", EvidenceRequired: []string{"test_result"},
			Checks: []standards.Check{{Type: "test", Assertion: "확인한다"}}}}}
}

func staleFixture(t *testing.T) (*Store, standards.Profile) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.CreateProject(t.Context(), model.Project{ID: "PRJ-1", Name: "p", RepositoryPath: "/r",
		DefaultBranch: "main", Provider: "codex", Model: "sonnet", WIPLimit: 1}); err != nil {
		t.Fatal(err)
	}
	profile := standards.Profile{ProjectID: "PRJ-1", PackRef: packWith(1).Ref()}
	if err = s.SaveStandardProfile(t.Context(), profile, packWith(1)); err != nil {
		t.Fatal(err)
	}
	return s, profile
}

// A criterion that was revised after it was judged has not been judged. The
// text asked a different question, so the stored MET answers a question nobody
// is asking — and counting it as met is how a fleet report stays green through
// a catalogue upgrade that moved the bar.
func TestAnAssessmentOfASupersededCriterionIsNotCountedAsMet(t *testing.T) {
	s, profile := staleFixture(t)
	ctx := t.Context()
	old := packWith(1)
	if _, err := s.RecordAssessment(ctx, old.Standards[0], standards.Assessment{ProjectID: "PRJ-1",
		StandardID: "T-001", CommitSHA: "abc", Result: standards.ResultMet, Detail: "통과",
		Evidence:   []standards.Evidence{{Kind: "test_result", Detail: "go test", Observed: true}},
		AssessedAt: time.Now()}, false); err != nil {
		t.Fatal(err)
	}
	// Judged under revision 1; the catalogue now asks revision 2.
	revised := packWith(2)
	views, err := s.AssessmentsFor(ctx, "PRJ-1", revised, profile, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("views=%+v", views)
	}
	view := views[0]
	if !view.Stale {
		t.Fatal("the criterion moved under the assessment")
	}
	if view.Result == standards.ResultMet {
		t.Fatal("a judgement about the old text does not settle the new one")
	}
	if view.Result != standards.ResultUnknown {
		t.Fatalf("it is unknown until somebody looks again: %q", view.Result)
	}
	// What it used to say is kept. A board that forgets it was met reads as
	// never assessed, and an operator cannot tell a regression from an upgrade.
	if view.PriorResult != standards.ResultMet {
		t.Fatalf("prior=%q", view.PriorResult)
	}
	if view.Detail == "" {
		t.Fatal("the reader has to be told why it went back to unknown")
	}
}

// The fleet rolls up the same rule, because the fleet report is where somebody
// decides the whole estate is in good shape.
func TestTheFleetDoesNotCountASupersededAssessment(t *testing.T) {
	s, _ := staleFixture(t)
	ctx := t.Context()
	old := packWith(1)
	if _, err := s.RecordAssessment(ctx, old.Standards[0], standards.Assessment{ProjectID: "PRJ-1",
		StandardID: "T-001", CommitSHA: "abc", Result: standards.ResultMet, Detail: "통과",
		Evidence:   []standards.Evidence{{Kind: "test_result", Detail: "go test", Observed: true}},
		AssessedAt: time.Now()}, false); err != nil {
		t.Fatal(err)
	}
	report, err := s.Fleet(ctx, packWith(2), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Criteria) != 1 {
		t.Fatalf("criteria=%+v", report.Criteria)
	}
	if report.Criteria[0].Met != 0 {
		t.Fatalf("a superseded assessment is not a project meeting the bar: %+v", report.Criteria[0])
	}
	if report.Criteria[0].Unknown != 1 {
		t.Fatalf("it is a project nobody has judged against this revision: %+v", report.Criteria[0])
	}
}

// A criterion whose revision did not move still counts. Treating every
// assessment as stale would make the whole program report nothing is known.
func TestAnAssessmentOfTheCurrentCriterionStillCounts(t *testing.T) {
	s, profile := staleFixture(t)
	ctx := t.Context()
	pack := packWith(1)
	if _, err := s.RecordAssessment(ctx, pack.Standards[0], standards.Assessment{ProjectID: "PRJ-1",
		StandardID: "T-001", CommitSHA: "abc", Result: standards.ResultMet, Detail: "통과",
		Evidence:   []standards.Evidence{{Kind: "test_result", Detail: "go test", Observed: true}},
		AssessedAt: time.Now()}, false); err != nil {
		t.Fatal(err)
	}
	views, err := s.AssessmentsFor(ctx, "PRJ-1", pack, profile, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if views[0].Stale || views[0].Result != standards.ResultMet {
		t.Fatalf("view=%+v", views[0])
	}
	if views[0].PriorResult != "" {
		t.Fatalf("nothing was superseded: %q", views[0].PriorResult)
	}
}

// A project enrolled before checksums were recorded has an empty one. Reading
// that as drift would stop maintaining every project that predates the column
// — the additive migration's whole point is that old rows keep working — and
// the sweep now refuses to touch a drifted project, so this is the difference
// between an upgrade and an outage.
func TestAProfileWithNoRecordedChecksumHasNotDrifted(t *testing.T) {
	s, _ := staleFixture(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `UPDATE standard_profiles SET pack_checksum='' WHERE project_id=?`,
		"PRJ-1"); err != nil {
		t.Fatal(err)
	}
	drifted, err := s.PackDrift(ctx, "PRJ-1", packWith(1))
	if err != nil {
		t.Fatal(err)
	}
	if drifted {
		t.Fatal("nothing was recorded, so nothing disagrees")
	}
}

// And a checksum that does disagree is drift, whatever the ref says.
func TestAPackEditedInPlaceIsDrift(t *testing.T) {
	s, _ := staleFixture(t)
	ctx := t.Context()
	edited := packWith(1)
	// Same id, same version, different content: the ref still resolves and the
	// checksum is the only thing that notices.
	edited.Standards[0].Title = "제자리에서 바뀐 제목"
	if edited.Ref() != packWith(1).Ref() {
		t.Fatalf("the test needs the ref to be unchanged: %s", edited.Ref())
	}
	drifted, err := s.PackDrift(ctx, "PRJ-1", edited)
	if err != nil {
		t.Fatal(err)
	}
	if !drifted {
		t.Fatal("the project agreed to different content")
	}
}

// The board has to tell the two apart: a criterion nobody has assessed needs a
// check run, and one whose waiver aged out needs somebody to decide whether the
// waiver still holds. Both were reported as "아직 확인하지 않았습니다".
func TestACriterionWhoseExceptionLapsedSaysSo(t *testing.T) {
	s, _ := staleFixture(t)
	ctx := t.Context()
	pack := packWith(1)
	decided := time.Now().AddDate(-2, 0, 0)
	profile := standards.Profile{ProjectID: "PRJ-1", PackRef: pack.Ref(),
		Exceptions: []standards.Exception{{StandardID: "T-001", Reason: "내부 레지스트리 반입 전",
			Decider: "hkjang", ReviewWhen: "반입 완료 시", DecidedAt: decided}}}
	if err := s.SaveStandardProfile(ctx, profile, pack); err != nil {
		t.Fatal(err)
	}
	views, err := s.AssessmentsFor(ctx, "PRJ-1", pack, profile, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("views=%+v", views)
	}
	view := views[0]
	if view.Result != standards.ResultUnknown {
		t.Fatalf("the waiver lapsed, so nothing is settled: %q", view.Result)
	}
	// The condition somebody has to check, and who decided it, both travel.
	for _, want := range []string{"반입 완료 시", "hkjang"} {
		if !strings.Contains(view.Detail, want) {
			t.Fatalf("detail must carry %q: %q", want, view.Detail)
		}
	}
	if strings.Contains(view.Detail, "아직 확인하지 않았습니다") {
		t.Fatalf("this is not an unassessed criterion: %q", view.Detail)
	}
}
