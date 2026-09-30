package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/standards"
)

func standardsFixture(t *testing.T) (context.Context, *Store, standards.Pack) {
	t.Helper()
	ctx, s, _, _ := boardFixture(t)
	return ctx, s, standards.GoReactOfflineService()
}

func reactProfile(pack standards.Pack, exceptions ...standards.Exception) standards.Profile {
	return standards.Profile{ProjectID: "PRJ-1", PackRef: pack.Ref(),
		Attributes: map[string]string{"frontend": "react", "network": "offline", "deployment": "service"}, Exceptions: exceptions}
}

// A profile holding an exception nobody owns, or a required criterion excepted
// with no review condition, must not reach the database. Once it is stored it
// looks like a decision somebody made.
func TestAnInvalidProfileIsNotStored(t *testing.T) {
	ctx, s, pack := standardsFixture(t)
	bad := reactProfile(pack, standards.Exception{StandardID: "NET-001", Reason: "나중에"})
	if err := s.SaveStandardProfile(ctx, bad, pack); err == nil {
		t.Fatal("an exception with no decider must be refused")
	}
	if _, _, err := s.StandardProfile(ctx, "PRJ-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nothing must have been written: %v", err)
	}
	good := reactProfile(pack, standards.Exception{StandardID: "NET-001", Reason: "1단계 범위 밖",
		Decider: "hkjang", ReviewWhen: "폐쇄망 반입 시작"})
	if err := s.SaveStandardProfile(ctx, good, pack); err != nil {
		t.Fatal(err)
	}
	loaded, checksum, err := s.StandardProfile(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PackRef != pack.Ref() || checksum != pack.Checksum() {
		t.Fatalf("profile=%+v checksum=%s", loaded, checksum)
	}
	if len(loaded.Exceptions) != 1 || loaded.Exceptions[0].Decider != "hkjang" {
		t.Fatalf("exceptions did not survive the round trip: %+v", loaded.Exceptions)
	}
	if loaded.Attributes["frontend"] != "react" {
		t.Fatalf("attributes did not survive: %+v", loaded.Attributes)
	}
}

// A pack edited in place keeps its version. Without a checksum the project
// would be judged against criteria it never agreed to while its profile still
// says 0.1.
func TestAnEditedPackIsDetectedEvenAtTheSameVersion(t *testing.T) {
	ctx, s, pack := standardsFixture(t)
	if err := s.SaveStandardProfile(ctx, reactProfile(pack), pack); err != nil {
		t.Fatal(err)
	}
	drifted, err := s.PackDrift(ctx, "PRJ-1", pack)
	if err != nil || drifted {
		t.Fatalf("the pinned pack is unchanged: %v %v", drifted, err)
	}
	edited := pack
	edited.Standards = append([]standards.Standard{}, pack.Standards...)
	edited.Standards[0].Intent = "quietly changed"
	if drifted, err = s.PackDrift(ctx, "PRJ-1", edited); err != nil || !drifted {
		t.Fatalf("an in-place edit at the same version must be detected: %v %v", drifted, err)
	}
}

// The judge runs at the store, so the rule holds however the assessment was
// produced — by the observer, by a model, or by a person filling in a form.
func TestTheStoreWillNotRecordAGuessAsMet(t *testing.T) {
	ctx, s, pack := standardsFixture(t)
	standard, _ := pack.Standard("UX-004")
	inferred := standards.Assessment{ProjectID: "PRJ-1", StandardID: "UX-004", CommitSHA: "abc123",
		Result: standards.ResultMet, ToolVersion: "t1",
		Evidence: []standards.Evidence{
			{Kind: "commit_sha", Detail: "abc123", Observed: true},
			{Kind: "route", Detail: "/projects/:id", Observed: true},
			{Kind: "browser_test_result", Detail: "라우터를 보니 될 것 같습니다", Observed: false},
			{Kind: "screenshot", Detail: "shot.png", Observed: true},
		}}
	saved, err := s.RecordAssessment(ctx, standard, inferred, false)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Result != standards.ResultUnknown {
		t.Fatalf("an inference must not be stored as met: %s", saved.Result)
	}
	if !strings.Contains(saved.Detail, "추정") {
		t.Fatalf("the stored detail must say why: %q", saved.Detail)
	}
}

// An assessment not pinned to a commit cannot be re-checked or aged out, and
// would be applied to code it never saw.
func TestAnAssessmentMustNameItsCommit(t *testing.T) {
	ctx, s, pack := standardsFixture(t)
	standard, _ := pack.Standard("UX-004")
	_, err := s.RecordAssessment(ctx, standard, standards.Assessment{ProjectID: "PRJ-1", StandardID: "UX-004",
		Result: standards.ResultUnmet}, false)
	if err == nil {
		t.Fatal("an assessment with no commit must be refused")
	}
}

// A list that only shows what was measured reads as complete coverage of a
// catalogue nobody finished walking.
func TestUnmeasuredCriteriaAreReportedNotOmitted(t *testing.T) {
	ctx, s, pack := standardsFixture(t)
	profile := reactProfile(pack, standards.Exception{StandardID: "NET-002", Reason: "자산 반입 예정",
		Decider: "hkjang", ReviewWhen: "반입 완료 시"})
	if err := s.SaveStandardProfile(ctx, profile, pack); err != nil {
		t.Fatal(err)
	}
	standard, _ := pack.Standard("UX-004")
	if _, err := s.RecordAssessment(ctx, standard, standards.Assessment{ProjectID: "PRJ-1", StandardID: "UX-004",
		CommitSHA: "abc123", Result: standards.ResultUnmet, Detail: "새로고침 시 404"}, false); err != nil {
		t.Fatal(err)
	}
	views, err := s.AssessmentsFor(ctx, "PRJ-1", pack, profile, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AssessmentView{}
	for _, view := range views {
		byID[view.StandardID] = view
	}
	if byID["UX-004"].Result != standards.ResultUnmet {
		t.Fatalf("the measured one keeps its result: %+v", byID["UX-004"])
	}
	if byID["CFG-001"].Result != standards.ResultUnknown {
		t.Fatalf("a criterion nobody looked at is UNKNOWN, not absent: %+v", byID["CFG-001"])
	}
	if byID["NET-002"].Result != standards.ResultNotApplicable {
		t.Fatalf("an excepted criterion reports the exception: %+v", byID["NET-002"])
	}
	if !strings.Contains(byID["NET-002"].Detail, "hkjang") {
		t.Fatalf("the exception must name who decided: %q", byID["NET-002"].Detail)
	}
	// AI-001 does not apply to this project and is not a gap it has.
	if _, present := byID["AI-001"]; present {
		t.Fatal("a criterion outside the profile is not a gap")
	}
}

// An assessment made against an older revision of a criterion still stands for
// what it measured; it just no longer answers the question being asked.
func TestAnAssessmentAgainstAnOldRevisionIsMarkedStale(t *testing.T) {
	ctx, s, pack := standardsFixture(t)
	profile := reactProfile(pack)
	standard, _ := pack.Standard("UX-004")
	if _, err := s.RecordAssessment(ctx, standard, standards.Assessment{ProjectID: "PRJ-1", StandardID: "UX-004",
		CommitSHA: "abc123", Result: standards.ResultUnmet}, false); err != nil {
		t.Fatal(err)
	}
	revised := pack
	revised.Standards = append([]standards.Standard{}, pack.Standards...)
	for i := range revised.Standards {
		if revised.Standards[i].ID == "UX-004" {
			revised.Standards[i].Revision = 2
		}
	}
	views, err := s.AssessmentsFor(ctx, "PRJ-1", revised, profile, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.StandardID == "UX-004" {
			if !view.Stale {
				t.Fatal("an assessment against revision 1 does not answer revision 2")
			}
			return
		}
	}
	t.Fatal("UX-004 missing")
}

// The same gap arriving as a new sentence every cycle is the failure this key
// exists to stop.
func TestTheSameDefectCannotBeFiledTwice(t *testing.T) {
	ctx, s, _ := standardsFixture(t)
	if err := s.FileFinding(ctx, "PRJ-1", "UX-004", "missing_route_restore", "web/src/routes", "WORK-1"); err != nil {
		t.Fatal(err)
	}
	err := s.FileFinding(ctx, "PRJ-1", "UX-004", "missing_route_restore", "web/src/routes", "WORK-2")
	if !errors.Is(err, ErrDuplicateFinding) {
		t.Fatalf("the same defect must not be filed twice: %v", err)
	}
	if !strings.Contains(err.Error(), "WORK-1") {
		t.Fatalf("the refusal must point at the item already filed: %v", err)
	}
	// A different defect against the same criterion is a different finding.
	if err := s.FileFinding(ctx, "PRJ-1", "UX-004", "missing_filter_restore", "web/src/routes", "WORK-3"); err != nil {
		t.Fatal(err)
	}
	existing, err := s.FindingFor(ctx, "PRJ-1", "UX-004", "missing_route_restore", "web/src/routes")
	if err != nil || existing != "WORK-1" {
		t.Fatalf("existing=%q err=%v", existing, err)
	}
}
