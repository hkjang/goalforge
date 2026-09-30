package observer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func verifyFixture(t *testing.T) (context.Context, *store.Store, standards.Pack, standards.Profile) {
	t.Helper()
	ctx, db, _ := supplyFixture(t)
	pack := standards.GoReactOfflineService()
	profile := standards.Profile{ProjectID: "PRJ-1", PackRef: pack.Ref(),
		Attributes: map[string]string{"frontend": "react", "network": "offline", "deployment": "service"}}
	if err := db.SaveStandardProfile(ctx, profile, pack); err != nil {
		t.Fatal(err)
	}
	return ctx, db, pack, profile
}

// A passing gate is the only thing that can settle a criterion upward, and
// until one claims a criterion nothing can. That is the whole reason the
// static observer reports UNKNOWN rather than MET.
func TestAGateThatClaimsACriterionCanSettleIt(t *testing.T) {
	ctx, db, pack, profile := verifyFixture(t)
	// The gate exercises a route and captures a screenshot, so it says so:
	// UX-004 asks for both and a journey result alone leaves them unobserved.
	if err := db.SetGateSettles(ctx, "PRJ-1", "route_restore", []string{"UX-004"}, "route", "screenshot"); err != nil {
		t.Fatal(err)
	}
	if err := recordGate(ctx, db, goalOf(t, ctx, db), store.VerificationRecord{
		CheckType: "route_restore", Status: "PASSED", ActualValue: "true", Required: true,
		EvidenceKind: "journey", Output: "journey.spec.ts 12 passed"}); err != nil {
		t.Fatal(err)
	}
	settled, err := SettleFromGates(ctx, db, "PRJ-1", goalOf(t, ctx, db), "abc123", pack, profile, "test")
	if err != nil {
		t.Fatal(err)
	}
	if settled["UX-004"] != standards.ResultMet {
		t.Fatalf("a passing journey gate that claims UX-004 settles it: %+v", settled)
	}
	views, err := db.AssessmentsFor(ctx, "PRJ-1", pack, profile, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.StandardID != "UX-004" {
			continue
		}
		if view.Result != standards.ResultMet {
			t.Fatalf("the assessment must be stored: %+v", view)
		}
		return
	}
	t.Fatal("UX-004 missing")
}

// A criterion nothing claims stays unsettled however many gates pass. Letting
// any journey gate settle any journey criterion is the same mistake as letting
// a build gate settle a journey one, moved up a level: the screen is built,
// something green ran, and nobody checked that the green thing was about this.
func TestAGateDoesNotSettleACriterionItDoesNotClaim(t *testing.T) {
	ctx, db, pack, profile := verifyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "some_other_journey", []string{"QA-001"}, "route"); err != nil {
		t.Fatal(err)
	}
	if err := recordGate(ctx, db, goalOf(t, ctx, db), store.VerificationRecord{
		CheckType: "some_other_journey", Status: "PASSED", ActualValue: "true", Required: true,
		EvidenceKind: "journey", Output: "ok"}); err != nil {
		t.Fatal(err)
	}
	settled, err := SettleFromGates(ctx, db, "PRJ-1", goalOf(t, ctx, db), "abc123", pack, profile, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed := settled["UX-004"]; claimed {
		t.Fatalf("a journey gate about something else does not settle UX-004: %+v", settled)
	}
	if settled["QA-001"] != standards.ResultMet {
		t.Fatalf("the criterion it does claim is settled: %+v", settled)
	}
}

// A gate of the wrong kind cannot settle a criterion even when it names it.
// Someone wiring a build gate to a journey criterion is making the original
// mistake explicitly, and the explicit version must be refused too.
func TestAGateOfTheWrongKindCannotSettleACriterion(t *testing.T) {
	ctx, db, pack, profile := verifyFixture(t)
	// The explicit version of the mistake: a build gate declaring it produces
	// a browser test result. Without naming that kind the gate would fall
	// short anyway for want of it, and a test built on that would pass with
	// the kind check deleted.
	if err := db.SetGateSettles(ctx, "PRJ-1", "compile", []string{"UX-004"},
		"browser_test_result", "route", "screenshot"); err != nil {
		t.Fatal(err)
	}
	if err := recordGate(ctx, db, goalOf(t, ctx, db), store.VerificationRecord{
		CheckType: "compile", Status: "PASSED", ActualValue: "true", Required: true,
		EvidenceKind: "build", Output: "ok"}); err != nil {
		t.Fatal(err)
	}
	settled, err := SettleFromGates(ctx, db, "PRJ-1", goalOf(t, ctx, db), "abc123", pack, profile, "test")
	if err != nil {
		t.Fatal(err)
	}
	if settled["UX-004"] == standards.ResultMet {
		t.Fatal("the code compiling says nothing about whether the page restores its filters")
	}
	// And a journey gate declaring the same kinds does settle it, so the
	// refusal above is about the gate's kind and not about the declaration.
	if err := db.SetGateSettles(ctx, "PRJ-1", "real_journey", []string{"UX-004"},
		"browser_test_result", "route", "screenshot"); err != nil {
		t.Fatal(err)
	}
	if err := recordGate(ctx, db, goalOf(t, ctx, db), store.VerificationRecord{
		CheckType: "real_journey", Status: "PASSED", ActualValue: "true", Required: true,
		EvidenceKind: "journey", Output: "ok"}); err != nil {
		t.Fatal(err)
	}
	if settled, err = SettleFromGates(ctx, db, "PRJ-1", goalOf(t, ctx, db), "abc123", pack, profile, "test"); err != nil {
		t.Fatal(err)
	}
	if settled["UX-004"] != standards.ResultMet {
		t.Fatalf("a journey gate declaring the same kinds does settle it: %+v", settled)
	}
}

// A claim naming a criterion the pack does not contain settles nothing and
// hides a typo that looks like coverage: the gate reports green, the evidence
// it claims to produce settles nothing, and nobody connects the two.
func TestAClaimForAnUnknownCriterionIsRefused(t *testing.T) {
	ctx, db, _, _ := verifyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "route_restore", []string{"UX-044"}); err == nil {
		t.Fatal("a claim for a criterion that does not exist must be refused")
	}
	if err := db.SetGateSettles(ctx, "PRJ-1", "route_restore", []string{"UX-004"}, "browser_test_reslt"); err == nil {
		t.Fatal("a misspelled evidence kind must be refused too")
	}
	if err := db.SetGateSettles(ctx, "PRJ-1", "route_restore", []string{"UX-004"}, "screenshot"); err != nil {
		t.Fatal(err)
	}
}

// A failing gate settles the criterion downward, which is the other half of
// what makes running things worth doing.
func TestAFailingGateMakesTheCriterionUnmet(t *testing.T) {
	ctx, db, pack, profile := verifyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "route_restore", []string{"UX-004"}); err != nil {
		t.Fatal(err)
	}
	if err := recordGate(ctx, db, goalOf(t, ctx, db), store.VerificationRecord{
		CheckType: "route_restore", Status: "FAILED", ActualValue: "false", Required: true,
		EvidenceKind: "journey", Output: "expected /projects/1, got /"}); err != nil {
		t.Fatal(err)
	}
	settled, err := SettleFromGates(ctx, db, "PRJ-1", goalOf(t, ctx, db), "abc123", pack, profile, "test")
	if err != nil {
		t.Fatal(err)
	}
	if settled["UX-004"] != standards.ResultUnmet {
		t.Fatalf("settled=%+v", settled)
	}
	views, err := db.AssessmentsFor(ctx, "PRJ-1", pack, profile, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.StandardID == "UX-004" && !strings.Contains(view.Detail, "route_restore") {
			t.Fatalf("the failure must name the gate that failed: %q", view.Detail)
		}
	}
}

// A criterion needing several kinds of evidence is not settled by one of them.
// UX-004 asks for a route and a screenshot as well as a journey result, and a
// journey gate alone leaves the rest unobserved.
func TestPartialEvidenceDoesNotSettleACriterion(t *testing.T) {
	ctx, db, pack, profile := verifyFixture(t)
	performance, ok := pack.Standard("AI-003")
	if !ok {
		t.Fatal("AI-003 missing")
	}
	_ = performance
	if err := db.SetGateSettles(ctx, "PRJ-1", "route_restore", []string{"UX-004"}); err != nil {
		t.Fatal(err)
	}
	// A journey result but no screenshot: UX-004 asks for both.
	if err := recordGate(ctx, db, goalOf(t, ctx, db), store.VerificationRecord{
		CheckType: "route_restore", Status: "PASSED", ActualValue: "true", Required: true,
		EvidenceKind: "journey", Output: "ok"}); err != nil {
		t.Fatal(err)
	}
	settled, err := SettleFromGates(ctx, db, "PRJ-1", goalOf(t, ctx, db), "abc123", pack, profile, "test")
	if err != nil {
		t.Fatal(err)
	}
	if settled["UX-004"] == standards.ResultMet {
		t.Fatal("UX-004 asks for a screenshot too, and nobody produced one")
	}
	if settled["UX-004"] != standards.ResultUnknown {
		t.Fatalf("settled=%+v", settled)
	}
}

func goalOf(t *testing.T, ctx context.Context, db *store.Store) string {
	t.Helper()
	goal, err := db.CurrentGoal(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	return goal.ID
}

// recordGate stores one gate result as evidence not tied to a run, which is
// what a standards verification pass produces.
func recordGate(ctx context.Context, db *store.Store, goalID string, record store.VerificationRecord) error {
	return db.RecordIntegrationEvidence(ctx, goalID, "abc123", []store.VerificationRecord{record})
}

// Configuring a gate is not running one. A criterion claimed by a gate nobody
// has executed must stay exactly where it was — treating "not run" as a pass
// would let a project settle its criteria by editing configuration, and
// treating it as a failure would report work nobody has checked as broken.
func TestAGateThatNeverRanSettlesNothing(t *testing.T) {
	ctx, db, pack, profile := verifyFixture(t)
	if err := db.SetGateSettles(ctx, "PRJ-1", "route_restore", []string{"UX-004"},
		"browser_test_result", "route", "screenshot"); err != nil {
		t.Fatal(err)
	}
	settled, err := SettleFromGates(ctx, db, "PRJ-1", goalOf(t, ctx, db), "abc123", pack, profile, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(settled) != 0 {
		t.Fatalf("a gate nobody ran settles nothing: %+v", settled)
	}
	views, err := db.AssessmentsFor(ctx, "PRJ-1", pack, profile, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.StandardID == "UX-004" && view.Result != standards.ResultUnknown {
			t.Fatalf("UX-004 must be untouched: %+v", view)
		}
	}
}

// A criterion a passing gate has settled is not a gap, and filing it as work
// would send someone to fix something that was just proven to work.
func TestAPassingGateKeepsTheCriterionOffTheBoard(t *testing.T) {
	ctx, db, _, _ := verifyFixture(t)
	goalID := goalOf(t, ctx, db)
	req := passRequest(t, ctx, db, goalID)
	// NET-002 is genuinely broken in this repository; the static read finds it.
	first, err := RunScheduledPass(ctx, db, req)
	if err != nil {
		t.Fatal(err)
	}
	filedNET := false
	for _, id := range first.Result.Filed {
		item, itemErr := db.WorkItemByID(ctx, goalID, id)
		if itemErr != nil {
			t.Fatal(itemErr)
		}
		if strings.HasPrefix(item.Title, "NET-002") {
			filedNET = true
		}
	}
	if !filedNET {
		t.Fatal("the static read finds NET-002 in this repository")
	}
	// CFG-001 is also broken, but a gate now proves it passes — a contrived
	// pairing, and exactly the case where the two methods disagree.
	ctx2, db2, _ := supplyFixture(t)
	req2 := passRequest(t, ctx2, db2, goalOf2(t, ctx2, db2))
	if err = db2.SetGateSettles(ctx2, "PRJ-1", "boot", []string{"CFG-001"}); err != nil {
		t.Fatal(err)
	}
	if err = recordGate(ctx2, db2, goalOf2(t, ctx2, db2), store.VerificationRecord{
		CheckType: "boot", Status: "PASSED", ActualValue: "true", Required: true,
		EvidenceKind: "integration", Output: "네 변수만으로 기동했습니다"}); err != nil {
		t.Fatal(err)
	}
	result, err := RunScheduledPass(ctx2, db2, req2)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range result.Result.Filed {
		item, itemErr := db2.WorkItemByID(ctx2, goalOf2(t, ctx2, db2), id)
		if itemErr != nil {
			t.Fatal(itemErr)
		}
		if strings.HasPrefix(item.Title, "CFG-001") {
			t.Fatal("a criterion a passing gate proved must not be filed as a gap")
		}
	}
}

func goalOf2(t *testing.T, ctx context.Context, db *store.Store) string {
	t.Helper()
	goal, err := db.CurrentGoal(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	return goal.ID
}
