package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/rrsi"
)

func selectionFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.CreateProject(t.Context(), model.Project{ID: "PRJ-1", Name: "p", RepositoryPath: "/r",
		DefaultBranch: "main", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	return s, "PRJ-1"
}

// A project with no policy gets one that refuses to judge. A missing policy
// read as "use the usual band" would apply another project's variance to this
// project's numbers.
func TestAProjectWithoutAPolicyCannotJudge(t *testing.T) {
	s, projectID := selectionFixture(t)
	policy, err := s.SelectionPolicyFor(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Calibrated() {
		t.Fatalf("a project nobody calibrated must not have a band: %+v", policy)
	}
	if policy.MinTrials == 0 || policy.Beta1 == 0 {
		t.Fatalf("everything except the band has a defensible default: %+v", policy)
	}
}

// The band is per project, so one project's measurement cannot be read as
// another's.
func TestTheBandIsPerProject(t *testing.T) {
	s, projectID := selectionFixture(t)
	ctx := t.Context()
	if err := s.CreateProject(ctx, model.Project{ID: "PRJ-2", Name: "q", RepositoryPath: "/r2",
		DefaultBranch: "main", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	policy := rrsi.DefaultPolicy()
	policy.NoiseBand, policy.CalibratedFrom = 0.042, "사례 10개 · 시행 50회"
	if err := s.SaveSelectionPolicy(ctx, projectID, policy); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.SelectionPolicyFor(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NoiseBand != 0.042 || loaded.CalibratedFrom == "" {
		t.Fatalf("policy did not survive the round trip: %+v", loaded)
	}
	other, err := s.SelectionPolicyFor(ctx, "PRJ-2")
	if err != nil {
		t.Fatal(err)
	}
	if other.Calibrated() {
		t.Fatalf("PRJ-2 was never calibrated: %+v", other)
	}
}

// Calibration reads one configuration's repetitions. Pooling across
// configurations would measure how much the configurations differ — the thing
// the band is meant to be compared against — so the measurement would contain
// its own answer.
func TestCalibrationReadsOneConfigurationOnly(t *testing.T) {
	s, projectID := selectionFixture(t)
	ctx := t.Context()
	for i := 1; i <= 3; i++ {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_cases(id,project_id,name,kind,created_at) VALUES(?,?,?,'feature','2026-10-01T00:00:00Z')`,
			"CASE-"+string(rune('0'+i)), projectID, "case-"+string(rune('0'+i))); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(condition, caseID, status string, repetition int) {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_trials(id,case_id,label,repetition,condition_hash,status,created_at)
VALUES(?,?,?,?,?,?,'2026-10-01T00:00:00Z')`,
			NewID("TRL"), caseID, condition, repetition, condition, status); err != nil {
			t.Fatal(err)
		}
	}
	// Two configurations, each with two repetitions of one case.
	seed("cond-a", "CASE-1", "PASSED", 1)
	seed("cond-a", "CASE-1", "FAILED", 2)
	seed("cond-b", "CASE-1", "PASSED", 1)
	seed("cond-b", "CASE-1", "PASSED", 2)
	trials, err := s.CalibrationTrials(ctx, "cond-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(trials) != 1 || len(trials[0].Outcomes) != 2 {
		t.Fatalf("only cond-a's own repetitions: %+v", trials)
	}
	if trials[0].Outcomes[0] == trials[0].Outcomes[1] {
		t.Fatalf("cond-a disagreed with itself and that is the point: %+v", trials[0])
	}
}

// A trial that errored or was invalid says nothing about whether the
// configuration passes, so it is not part of the spread. Counting it as a
// failure would widen the band with the evaluator's own problems.
func TestUnusableTrialsAreNotPartOfTheSpread(t *testing.T) {
	s, projectID := selectionFixture(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_cases(id,project_id,name,kind,created_at) VALUES('CASE-1',?,'case-1','feature','2026-10-01T00:00:00Z')`, projectID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"PASSED", "FAILED", "ERRORED", "INVALID"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_trials(id,case_id,label,repetition,condition_hash,status,created_at)
VALUES(?,'CASE-1','a',1,'cond-a',?,'2026-10-01T00:00:00Z')`, NewID("TRL"), status); err != nil {
			t.Fatal(err)
		}
	}
	trials, err := s.CalibrationTrials(ctx, "cond-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(trials) != 1 || len(trials[0].Outcomes) != 2 {
		t.Fatalf("only the two that measured something: %+v", trials)
	}
}
