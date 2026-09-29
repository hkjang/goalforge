package observer

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func supplyFixture(t *testing.T) (context.Context, *store.Store, model.Goal) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = db.CreateProject(ctx, model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo",
		DefaultBranch: "main", Provider: "codex", WIPLimit: 1}); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, "PRJ-1", "G", "o", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, db, goal
}

func observationOf(findings ...Finding) Observation {
	return Observation{ProjectID: "PRJ-1", CommitSHA: "abc123", ToolVersion: "t1", Findings: findings}
}

func unmet(standardID, defect, scope string) Finding {
	return Finding{StandardID: standardID, Result: standards.ResultUnmet, DefectKind: defect,
		TargetScope: scope, Detail: standardID + " 미충족"}
}

// The point of the whole pass. The same gap must not arrive as a new card every
// cycle, however the report is worded the second time.
func TestASecondPassDoesNotRefileTheSameGap(t *testing.T) {
	ctx, db, goal := supplyFixture(t)
	pack := standards.GoReactOfflineService()
	first, err := Supply(ctx, db, goal.ID, observationOf(unmet("NET-002", "external_asset_reference", "web/**")),
		pack, DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Filed) != 1 {
		t.Fatalf("filed=%v", first.Filed)
	}
	// A second pass over a repository that has not changed. The detail is
	// worded differently, as a generator's would be.
	reworded := unmet("NET-002", "external_asset_reference", "web/**")
	reworded.Detail = "외부 폰트를 계속 불러오고 있습니다"
	second, err := Supply(ctx, db, goal.ID, observationOf(reworded), pack, DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Filed) != 0 {
		t.Fatalf("the same defect must not be filed twice: %v", second.Filed)
	}
	if second.AlreadyFiled["NET-002"] != first.Filed[0] {
		t.Fatalf("the pass must say it found the gap and chose not to duplicate it: %+v", second.AlreadyFiled)
	}
}

// An investigation is not a defect. Filing UNKNOWN as work would put "go and
// look at this" on the board as if it were a fix.
func TestAnUnknownIsNotFiledAsWork(t *testing.T) {
	ctx, db, goal := supplyFixture(t)
	observation := observationOf(Finding{StandardID: "CORE-001", Result: standards.ResultUnknown,
		Detail: "실행해서 확인해야 합니다"})
	result, err := Supply(ctx, db, goal.ID, observation, standards.GoReactOfflineService(), DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Filed) != 0 {
		t.Fatalf("an unknown is an investigation, not a defect: %v", result.Filed)
	}
}

// Required criteria are filed first, and what does not fit is held rather than
// dropped. A finding dropped on the floor is one the next pass has to
// rediscover, and the pass after that.
func TestTheCapKeepsTheRequiredOnesAndHoldsTheRest(t *testing.T) {
	ctx, db, goal := supplyFixture(t)
	pack := standards.GoReactOfflineService()
	// DOC-001 and UX-003 are recommended; NET-002 and CFG-001 are required.
	// The recommended ones arrive first, as a detector ordering easily could.
	result, err := Supply(ctx, db, goal.ID, observationOf(
		unmet("DOC-001", "missing_pages", "docs/**"),
		unmet("UX-003", "unreadable", "web/**"),
		unmet("NET-002", "external_asset_reference", "web/**"),
		unmet("CFG-001", "extra_runtime_env", "**/*.go"),
	), pack, SupplyPolicy{PerRun: 2, MaxOutstanding: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Filed) != 2 {
		t.Fatalf("filed=%v", result.Filed)
	}
	titles := map[string]bool{}
	for _, id := range result.Filed {
		item, itemErr := db.WorkItemByID(ctx, goal.ID, id)
		if itemErr != nil {
			t.Fatal(itemErr)
		}
		titles[strings.SplitN(item.Title, ":", 2)[0]] = true
	}
	if !titles["NET-002"] || !titles["CFG-001"] {
		t.Fatalf("the required criteria must not lose to arrival order: %v", titles)
	}
	if len(result.Deferred) != 2 {
		t.Fatalf("what did not fit must be held, not lost: %v", result.Deferred)
	}
}

// A board nobody is working through does not need more findings on it.
func TestSupplyStopsWhileTheBoardIsFull(t *testing.T) {
	ctx, db, goal := supplyFixture(t)
	pack := standards.GoReactOfflineService()
	if _, err := Supply(ctx, db, goal.ID, observationOf(
		unmet("NET-002", "external_asset_reference", "web/**"),
		unmet("CFG-001", "extra_runtime_env", "**/*.go"),
	), pack, SupplyPolicy{PerRun: 5, MaxOutstanding: 10}); err != nil {
		t.Fatal(err)
	}
	_, err := Supply(ctx, db, goal.ID, observationOf(unmet("REL-001", "missing_image_definition", ".")),
		pack, SupplyPolicy{PerRun: 5, MaxOutstanding: 2})
	if !errors.Is(err, ErrSupplyPaused) {
		t.Fatalf("supply must pause while the board is full: %v", err)
	}
	// Finishing the outstanding work lets supply resume.
	for _, id := range []string{} {
		_ = id
	}
	items, err := db.ListWorkItems(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if _, err = db.ApplyManualTransition(ctx, goal.ID, item.ID, "DISCARDED", item.Version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = Supply(ctx, db, goal.ID, observationOf(unmet("REL-001", "missing_image_definition", ".")),
		pack, SupplyPolicy{PerRun: 5, MaxOutstanding: 2}); err != nil {
		t.Fatalf("a cleared board accepts supply again: %v", err)
	}
}

// A card saying only "UX-004 미충족" sends whoever picks it up back to the
// catalogue to find out what that means.
func TestTheCardCarriesWhatWasObservedAndWhatWouldSettleIt(t *testing.T) {
	ctx, db, goal := supplyFixture(t)
	pack := standards.GoReactOfflineService()
	finding := unmet("NET-002", "external_asset_reference", "web/**")
	finding.Detail = "런타임에 외부 자산을 불러옵니다: fonts.googleapis.com (web/index.html)"
	evidence, err := StaticEvidence("source_file", "web/index.html 가 fonts.googleapis.com 를 참조합니다")
	if err != nil {
		t.Fatal(err)
	}
	finding.Evidence = []standards.Evidence{evidence}
	result, err := Supply(ctx, db, goal.ID, observationOf(finding), pack, DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	item, err := db.WorkItemByID(ctx, goal.ID, result.Filed[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fonts.googleapis.com", "web/index.html", "관측"} {
		if !strings.Contains(item.Objective, want) {
			t.Fatalf("the objective must carry %q:\n%s", want, item.Objective)
		}
	}
	// The acceptance is the criterion's own checks, so the thing that files the
	// work and the thing that judges it read the same sentence.
	standard, _ := pack.Standard("NET-002")
	for _, check := range standard.Checks {
		if !strings.Contains(item.Acceptance, check.Assertion) {
			t.Fatalf("the acceptance must be the criterion's check:\n%s", item.Acceptance)
		}
	}
	if item.ChangeScope != "web/**" {
		t.Fatalf("the card must arrive scoped: %q", item.ChangeScope)
	}
	if item.Priority < 90 {
		t.Fatalf("a required criterion outranks a recommended one: %v", item.Priority)
	}
}
