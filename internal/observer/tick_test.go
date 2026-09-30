package observer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// tickProjectIn registers a project with a real repository holding real
// defects, so an unattended sweep has something to find.
func tickProjectIn(t *testing.T, ctx context.Context, db *store.Store, id string, broken bool) string {
	t.Helper()
	repo := tickRepoIn(t, ctx, db, id, broken)
	if _, err := db.SetGoal(ctx, id, "G", "o", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}}); err != nil {
		t.Fatal(err)
	}
	return repo
}

// tickRepoIn is the same registration without a goal, which is the state a
// project is in between `goalforge project add` and `goalforge goal set`.
func tickRepoIn(t *testing.T, ctx context.Context, db *store.Store, id string, broken bool) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "T"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	files := map[string]string{"go.mod": "module example.com/x\n",
		"web/package.json": `{"dependencies":{"react":"18.2.0"}}`}
	if broken {
		files["web/index.html"] = `<link href="https://fonts.googleapis.com/css2?family=Inter">`
	}
	for name, body := range files {
		full := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	if err := db.CreateProject(ctx, model.Project{ID: id, Name: id, RepositoryPath: repo,
		DefaultBranch: "main", Provider: "codex", WIPLimit: 1}); err != nil {
		t.Fatal(err)
	}
	return repo
}

func enrol(t *testing.T, ctx context.Context, db *store.Store, id string) {
	t.Helper()
	pack := standards.GoReactOfflineService()
	if err := db.SaveStandardProfile(ctx, standards.Profile{ProjectID: id, PackRef: pack.Ref(),
		Attributes: map[string]string{"frontend": "react", "network": "offline"}}, pack); err != nil {
		t.Fatal(err)
	}
}

func tickFixture(t *testing.T) (context.Context, *store.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return ctx, db
}

// The gap this exists for. Every part of the schedule — a daily interval, a
// discovery budget, an idempotency key that survives two workers seeing the
// same commit — is machinery for something that runs while nobody is watching.
func TestASweepSuppliesWorkWithoutAnybodyAskingIt(t *testing.T) {
	ctx, db := tickFixture(t)
	tickProjectIn(t, ctx, db, "PRJ-1", true)
	enrol(t, ctx, db, "PRJ-1")
	result, err := Tick(ctx, db, "test", Default(), DefaultSchedulePolicy(), DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 1 || !result.Projects[0].Ran {
		t.Fatalf("result=%+v", result.Projects)
	}
	if len(result.Projects[0].Filed) == 0 {
		t.Fatal("the repository has a CDN reference and nobody had to ask for it to be found")
	}
	if !result.Acted() {
		t.Fatal("a sweep that filed work acted")
	}
	filed := len(result.Projects[0].Filed)
	// Sweeping again does not put the same findings on the board a second
	// time. An unattended loop is the one place duplicate supply is invisible
	// until the board is unusable, because nobody was there for the first
	// sweep either.
	goal, err := db.CurrentGoal(ctx, "PRJ-1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err = Tick(ctx, db, "test", Default(), DefaultSchedulePolicy(), DefaultSupplyPolicy()); err != nil {
			t.Fatal(err)
		}
	}
	items, err := db.ListWorkItems(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != filed {
		t.Fatalf("four sweeps, one board: %d cards from %d findings", len(items), filed)
	}
	// With the backlog floor out of the way, an unchanged repository stops the
	// sweep from looking at all, and says why.
	schedule := DefaultSchedulePolicy()
	schedule.BacklogFloor = 0
	schedule.DiscoveryBudget = 0
	quiet, err := Tick(ctx, db, "test", Default(), schedule, DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if quiet.Projects[0].Ran {
		t.Fatalf("nothing changed: %+v", quiet.Projects[0])
	}
	if quiet.Projects[0].Note == "" {
		t.Fatal("a quiet tick must still say why")
	}
	if quiet.Acted() {
		t.Fatal("a sweep that did nothing must be able to say so, or every tick looks like activity")
	}
}

// A project that never pinned a pack has not asked to be maintained.
func TestASweepLeavesProjectsOutsideTheProgramAlone(t *testing.T) {
	ctx, db := tickFixture(t)
	tickProjectIn(t, ctx, db, "PRJ-1", true)
	enrol(t, ctx, db, "PRJ-1")
	tickProjectIn(t, ctx, db, "PRJ-2", true)
	result, err := Tick(ctx, db, "test", Default(), DefaultSchedulePolicy(), DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 1 || result.Projects[0].ProjectID != "PRJ-1" {
		t.Fatalf("result=%+v", result.Projects)
	}
}

// An unattended loop that aborts on the first problem stops maintaining every
// project because of one — and the one that broke it is the one nobody was
// watching.
func TestOneBrokenProjectDoesNotStopTheSweep(t *testing.T) {
	ctx, db := tickFixture(t)
	repo := tickProjectIn(t, ctx, db, "PRJ-1", true)
	enrol(t, ctx, db, "PRJ-1")
	tickProjectIn(t, ctx, db, "PRJ-2", true)
	enrol(t, ctx, db, "PRJ-2")
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	result, err := Tick(ctx, db, "test", Default(), DefaultSchedulePolicy(), DefaultSupplyPolicy())
	if err != nil {
		t.Fatalf("one missing repository must not fail the sweep: %v", err)
	}
	if len(result.Projects) != 2 {
		t.Fatalf("both projects must be reported: %+v", result.Projects)
	}
	var broken, healthy ProjectTick
	for _, project := range result.Projects {
		if project.ProjectID == "PRJ-1" {
			broken = project
		} else {
			healthy = project
		}
	}
	if broken.Err == nil {
		t.Fatal("the broken one must report its failure")
	}
	if healthy.Err != nil || len(healthy.Filed) == 0 {
		t.Fatalf("the other project is still maintained: %+v", healthy)
	}
}

// An envelope that only exists as arguments to a command somebody types
// applies exactly when somebody is already there — which is the one time it
// was not needed.
func TestTheSweepAppliesTheSavedEnvelope(t *testing.T) {
	ctx, db := tickFixture(t)
	tickProjectIn(t, ctx, db, "PRJ-1", true)
	enrol(t, ctx, db, "PRJ-1")
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveAutonomyConfig(ctx, store.AutonomyConfig{ProjectID: "PRJ-1", Enabled: true,
		AllStandards: true, AllScopes: true, DailyLimit: 5}); err != nil {
		t.Fatal(err)
	}
	result, err := Tick(ctx, db, "test", Default(), DefaultSchedulePolicy(), DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects[0].Approved) == 0 {
		t.Fatalf("the saved envelope must be applied without anybody asking: %+v", result.Projects[0])
	}
}

// A project with no saved envelope gets nothing approved. An unattended loop
// reading a missing configuration as permission is the worst possible reading
// of an absence.
func TestNoSavedEnvelopeMeansNoAutomaticApproval(t *testing.T) {
	ctx, db := tickFixture(t)
	tickProjectIn(t, ctx, db, "PRJ-1", true)
	enrol(t, ctx, db, "PRJ-1")
	if err := db.SetGateSettles(ctx, "PRJ-1", "asset_scan", []string{"NET-002"}); err != nil {
		t.Fatal(err)
	}
	result, err := Tick(ctx, db, "test", Default(), DefaultSchedulePolicy(), DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects[0].Approved) != 0 {
		t.Fatalf("no envelope means no permission: %+v", result.Projects[0])
	}
	// And one saved but switched off is the same answer.
	if err = db.SaveAutonomyConfig(ctx, store.AutonomyConfig{ProjectID: "PRJ-1", Enabled: false,
		AllStandards: true, AllScopes: true, DailyLimit: 5}); err != nil {
		t.Fatal(err)
	}
	if result, err = Tick(ctx, db, "test", Default(), DefaultSchedulePolicy(), DefaultSupplyPolicy()); err != nil {
		t.Fatal(err)
	}
	if len(result.Projects[0].Approved) != 0 {
		t.Fatalf("switched off is switched off: %+v", result.Projects[0])
	}
}

// A throttled project is the loop working as configured. Recording it as an
// error would make a correctly throttled project look broken, and an operator
// reading a list of failures would go looking for one that is not there.
func TestAThrottledProjectIsNotAFailure(t *testing.T) {
	ctx, db := tickFixture(t)
	tickProjectIn(t, ctx, db, "PRJ-1", true)
	enrol(t, ctx, db, "PRJ-1")
	schedule := DefaultSchedulePolicy()
	schedule.DiscoveryBudget = 1
	if _, err := Tick(ctx, db, "test", Default(), schedule, DefaultSupplyPolicy()); err != nil {
		t.Fatal(err)
	}
	result, err := Tick(ctx, db, "test", Default(), schedule, DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	tick := result.Projects[0]
	if tick.Err != nil {
		t.Fatalf("a spent budget is not a failure: %v", tick.Err)
	}
	if !strings.Contains(tick.Note, "budget") && !strings.Contains(tick.Note, "예산") {
		t.Fatalf("note=%q", tick.Note)
	}
}

// A project enrolled before its goal was set has nothing for the sweep to
// supply work against — but it is not broken. Reporting it as a failure makes
// the sweep print an error every quarter of an hour, forever, about a project
// nobody needs to fix, and an operator who scrolls past the log stops reading
// the line that matters.
func TestAnEnrolledProjectWithNoGoalIsQuietNotBroken(t *testing.T) {
	ctx, db := tickFixture(t)
	tickRepoIn(t, ctx, db, "PRJ-1", true)
	enrol(t, ctx, db, "PRJ-1")
	result, err := Tick(ctx, db, "test", Default(), DefaultSchedulePolicy(), DefaultSupplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 1 {
		t.Fatalf("result=%+v", result.Projects)
	}
	tick := result.Projects[0]
	if tick.Err != nil {
		t.Fatalf("a project waiting for its goal is not a failure: %v", tick.Err)
	}
	if !strings.Contains(tick.Note, "목표") {
		t.Fatalf("a quiet tick must still say why: note=%q", tick.Note)
	}
	if result.Acted() {
		t.Fatal("a sweep that did nothing must stay quiet, or every tick looks like activity")
	}
}

var _ = time.Now
