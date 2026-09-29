package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func modelStatsFixture(t *testing.T) (context.Context, *Store, model.Project) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	return ctx, s, project
}

// recordRun writes a run with the given outcome and however many usage rows a
// real run produces — a provider reports input, output, and cost separately,
// so one run routinely has several.
func recordRun(t *testing.T, ctx context.Context, s *Store, project model.Project, runID, modelName, state string, usageRows int, seconds float64) {
	t.Helper()
	if err := s.StartRun(ctx, RunRecord{ID: runID, ProjectID: project.ID, Provider: "codex",
		Model: modelName, TaskType: "IMPLEMENT_SELECTED"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < usageRows; i++ {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO usage_ledger(run_id,event_key,token_type,amount,cost,created_at) VALUES(?,?,?,?,?,?)`,
			runID, fmt.Sprintf("turn-%d", i), "input_tokens", 100, 0.01, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	terminal := "COMPLETED"
	if state != "COMPLETED" && state != "CHECKPOINTING" {
		terminal = "FAILED"
	}
	if err := s.FinishRun(ctx, runID, terminal, "READY"); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Duration(seconds) * time.Second)
	if _, err := s.db.ExecContext(ctx, `UPDATE runs SET state=?,started_at=?,ended_at=? WHERE id=?`,
		state, start.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), runID); err != nil {
		t.Fatal(err)
	}
}

func statFor(t *testing.T, stats []ModelStat, modelName string) ModelStat {
	t.Helper()
	for _, stat := range stats {
		if stat.Model == modelName {
			return stat
		}
	}
	t.Fatalf("no stat for %q in %+v", modelName, stats)
	return ModelStat{}
}

// A provider reports input, output, and cost separately, so one run routinely
// writes several usage rows. Counting the success once per joined row made the
// rate a multiple of how chatty the provider's accounting was — one successful
// run with three usage rows reported 300%.
func TestSuccessRateDoesNotCountUsageRows(t *testing.T) {
	ctx, s, project := modelStatsFixture(t)
	recordRun(t, ctx, s, project, "RUN-1", "haiku", "COMPLETED", 3, 10)
	stats, err := s.ModelStats(ctx, project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stat := statFor(t, stats, "haiku")
	if stat.Runs != 1 {
		t.Fatalf("one run: %+v", stat)
	}
	if stat.Verified != 1 {
		t.Fatalf("one success, however many usage rows it wrote: verified=%d", stat.Verified)
	}
	if stat.SuccessRate != 100 {
		t.Fatalf("success rate must be 100%%, got %.0f%%", stat.SuccessRate)
	}
}

// The rate must not depend on how many usage rows a run happened to write.
func TestSuccessRateIsIndependentOfUsageRowCount(t *testing.T) {
	ctx, s, project := modelStatsFixture(t)
	// Two runs, one success and one failure, with wildly different accounting.
	recordRun(t, ctx, s, project, "RUN-1", "haiku", "COMPLETED", 7, 10)
	recordRun(t, ctx, s, project, "RUN-2", "haiku", "FAILED", 1, 10)
	stats, err := s.ModelStats(ctx, project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stat := statFor(t, stats, "haiku")
	if stat.Runs != 2 || stat.Verified != 1 {
		t.Fatalf("two runs, one verified: %+v", stat)
	}
	if stat.SuccessRate != 50 {
		t.Fatalf("success rate must be 50%%, got %.1f%%", stat.SuccessRate)
	}
}

// Whatever the data, a rate outside 0–100 is a bug that will be read as a fact.
func TestSuccessRateStaysWithinBounds(t *testing.T) {
	ctx, s, project := modelStatsFixture(t)
	for i, rows := range []int{0, 1, 5, 12} {
		recordRun(t, ctx, s, project, "RUN-"+string(rune('A'+i)), "haiku", "COMPLETED", rows, 5)
	}
	stats, err := s.ModelStats(ctx, project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stat := statFor(t, stats, "haiku")
	if stat.SuccessRate < 0 || stat.SuccessRate > 100 {
		t.Fatalf("success rate out of bounds: %.1f%%", stat.SuccessRate)
	}
	if stat.Runs != 4 || stat.Verified != 4 {
		t.Fatalf("%+v", stat)
	}
}

// The duration average is over runs, not over usage rows: a run that wrote
// more accounting is not a longer run.
func TestAverageDurationWeightsRunsEqually(t *testing.T) {
	ctx, s, project := modelStatsFixture(t)
	recordRun(t, ctx, s, project, "RUN-1", "haiku", "COMPLETED", 9, 100)
	recordRun(t, ctx, s, project, "RUN-2", "haiku", "COMPLETED", 1, 0)
	stats, err := s.ModelStats(ctx, project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stat := statFor(t, stats, "haiku")
	// Two runs of 100s and ~0s average ~50s. Weighted by usage rows it would
	// be ~90s, which is the shape of the same bug.
	if stat.AverageSeconds < 40 || stat.AverageSeconds > 60 {
		t.Fatalf("average must weight runs equally, got %.1fs", stat.AverageSeconds)
	}
}

// Tokens and cost are totals across the project, so they do add up over usage
// rows — the fix must not turn a real sum into a wrong one.
func TestTokensAndCostStillTotalAcrossUsageRows(t *testing.T) {
	ctx, s, project := modelStatsFixture(t)
	recordRun(t, ctx, s, project, "RUN-1", "haiku", "COMPLETED", 3, 5)
	stats, err := s.ModelStats(ctx, project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stat := statFor(t, stats, "haiku")
	if stat.Tokens != 300 {
		t.Fatalf("three rows of 100 tokens total 300, got %d", stat.Tokens)
	}
	if stat.CostUSD < 0.029 || stat.CostUSD > 0.031 {
		t.Fatalf("three rows of $0.01 total $0.03, got %.4f", stat.CostUSD)
	}
}

// A run with no usage recorded still counts: the outer join must not drop it.
func TestRunsWithoutUsageStillCount(t *testing.T) {
	ctx, s, project := modelStatsFixture(t)
	recordRun(t, ctx, s, project, "RUN-1", "haiku", "COMPLETED", 0, 5)
	stats, err := s.ModelStats(ctx, project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stat := statFor(t, stats, "haiku")
	if stat.Runs != 1 || stat.Verified != 1 || stat.SuccessRate != 100 {
		t.Fatalf("%+v", stat)
	}
}
