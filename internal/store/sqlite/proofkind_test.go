package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func proofKindProject(t *testing.T) (context.Context, *Store, model.Project) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	return ctx, s, p
}

// AT-11: a screen is finished and the build is green, but the save button is
// wired to a stub. The criterion asks whether the user's task completes, so a
// passing build cannot answer it and the goal must not be judged complete.
func TestBuildEvidenceCannotSettleAJourneyCriterion(t *testing.T) {
	ctx, s, p := proofKindProject(t)
	g, err := s.SetGoal(ctx, p.ID, "Notes app", "user can save a note", "", []model.Criterion{
		{Type: "note_saves", ExpectedValue: "true", RequiredKind: "journey"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.Criteria[0].RequiredKind != "journey" {
		t.Fatalf("the demanded kind must survive a round trip: %+v", g.Criteria[0])
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO work_items(id,goal_id,type,title,status,weight) VALUES('W1',?,'IMPLEMENT','save screen','DONE',1)`, g.ID); err != nil {
		t.Fatal(err)
	}
	// The gate named after the criterion only compiles the project, and says so.
	if _, err = s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,check_type,status,actual_value,evidence_kind,created_at) VALUES(?,'note_saves','PASSED','true','build','now')`, g.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GoalProgressDetail(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Criteria[0].Status != "WRONG_KIND" || detail.Criteria[0].Satisfied {
		t.Fatalf("a build must not settle a journey criterion: %+v", detail.Criteria[0])
	}
	if detail.Complete {
		t.Fatal("the goal must not complete on evidence of the wrong kind")
	}
	if detail.Criteria[0].KindMismatch() == "" {
		t.Fatal("the user needs to be told which kind was asked for and which answered")
	}
	// The same criterion measured by a check that actually carries out the
	// user's task does settle it.
	if _, err = s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,check_type,status,actual_value,evidence_kind,created_at) VALUES(?,'note_saves','PASSED','true','journey','now')`, g.ID); err != nil {
		t.Fatal(err)
	}
	detail, err = s.GoalProgressDetail(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Criteria[0].Status != "MET" || !detail.Complete {
		t.Fatalf("journey evidence must settle it: %+v complete=%v", detail.Criteria[0], detail.Complete)
	}
}

// A journey check that fails leaves the criterion UNMET rather than WRONG_KIND:
// the right check ran, the feature does not work. The two need different fixes,
// so they must not report the same way.
func TestFailingJourneyIsUnmetNotMisconfigured(t *testing.T) {
	ctx, s, p := proofKindProject(t)
	g, err := s.SetGoal(ctx, p.ID, "Notes app", "user can save a note", "", []model.Criterion{
		{Type: "note_saves", ExpectedValue: "true", RequiredKind: "journey"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,check_type,status,actual_value,evidence_kind,created_at) VALUES(?,'note_saves','FAILED','false','journey','now')`, g.ID); err != nil {
		t.Fatal(err)
	}
	criteria, err := s.CriteriaStatus(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if criteria[0].Status != "UNMET" {
		t.Fatalf("a failing journey is a shortfall, not a misconfiguration: %+v", criteria[0])
	}
	if criteria[0].KindMismatch() != "" {
		t.Fatalf("no mismatch to explain: %q", criteria[0].KindMismatch())
	}
}

// Goals recorded before kinds existed demand nothing, so their evidence keeps
// settling them: adding the field must not retroactively unmet every project.
func TestCriteriaWithoutADemandedKindAreUnaffected(t *testing.T) {
	ctx, s, p := proofKindProject(t)
	g, err := s.SetGoal(ctx, p.ID, "Service", "ship", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,check_type,status,actual_value,created_at) VALUES(?,'build_passed','PASSED','true','now')`, g.ID); err != nil {
		t.Fatal(err)
	}
	criteria, err := s.CriteriaStatus(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if criteria[0].Status != "MET" {
		t.Fatalf("unclassified evidence still settles an undemanding criterion: %+v", criteria[0])
	}
}

// Relabelling what a gate proves invalidates the evidence taken under the old
// label: otherwise calling a compile a "journey" would turn old build results
// into journey proof retroactively.
func TestChangingAGateKindInvalidatesItsEvidence(t *testing.T) {
	ctx, s, p := proofKindProject(t)
	g, err := s.SetGoal(ctx, p.ID, "Service", "ship", "", []model.Criterion{{Type: "note_saves", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	gate := GateConfig{Type: "note_saves", Command: []string{"go", "build", "./..."}, Timeout: time.Minute, Required: true, SuccessValue: "true", Kind: "build"}
	if err = s.UpsertGate(ctx, p.ID, gate); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,check_type,status,actual_value,evidence_kind,created_at) VALUES(?,'note_saves','PASSED','true','build','now')`, g.ID); err != nil {
		t.Fatal(err)
	}
	gate.Kind = "journey"
	if err = s.UpsertGate(ctx, p.ID, gate); err != nil {
		t.Fatal(err)
	}
	criteria, err := s.CriteriaStatus(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if criteria[0].Status != "STALE" {
		t.Fatalf("evidence taken under the old label must be re-measured: %+v", criteria[0])
	}
}

func TestGateKindMustBeKnown(t *testing.T) {
	ctx, s, p := proofKindProject(t)
	err := s.UpsertGate(ctx, p.ID, GateConfig{Type: "t", Command: []string{"go"}, Timeout: time.Minute, Kind: "looks-fine"})
	if err == nil {
		t.Fatal("an unknown gate kind must be refused rather than silently stored")
	}
	if _, err = s.SetGoal(ctx, p.ID, "G", "o", "", []model.Criterion{{Type: "c", ExpectedValue: "true", RequiredKind: "looks-fine"}}); err == nil {
		t.Fatal("an unknown demanded kind must be refused")
	}
}

// The evaluator identity covers the kind, so refreshing evidence detects a
// relabelled gate even when the change did not come through UpsertGate.
func TestEvaluatorIdentityCoversTheKind(t *testing.T) {
	base := GateConfig{Type: "note_saves", Command: []string{"go", "test", "./..."}, Timeout: time.Minute, Required: true, SuccessValue: "true", Kind: "test"}
	relabelled := base
	relabelled.Kind = "journey"
	if EvaluatorID(base) == EvaluatorID(relabelled) {
		t.Fatal("two gates that claim to prove different things must not share an evaluator identity")
	}
}
