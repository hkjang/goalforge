package capture

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func captureFixture(t *testing.T) (context.Context, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "T"}} {
		if out, cmdErr := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); cmdErr != nil {
			t.Skipf("git unavailable: %v %s", cmdErr, out)
		}
	}
	if err = db.CreateProject(ctx, model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: repo,
		DefaultBranch: "main", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	return ctx, db, repo
}

func commitFile(t *testing.T, repo, name, body string) string {
	t.Helper()
	full := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "c " + name}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func page(route, role, state string, sources ...string) Page {
	return Page{Route: route, Role: role, State: state, Script: "docs/" + strings.Trim(route, "/"),
		Sources: sources, Required: true}
}

func manifestOf(pages ...Page) Manifest {
	return Manifest{BaseURL: "http://internal.example", SeedRef: "seed/demo", Pages: pages}
}

// A screenshot taken at an older commit is not automatically out of date.
// Marking it so would make every commit invalidate every picture, the report
// would be red permanently, and people would stop reading it — which costs
// more than the stale pictures it was meant to catch.
func TestACaptureIsStaleOnlyWhenItsOwnScreenChanged(t *testing.T) {
	ctx, db, repo := captureFixture(t)
	commitFile(t, repo, "web/src/routes/projects.tsx", "v1")
	taken := commitFile(t, repo, "README.md", "readme")
	manifest := manifestOf(page("/projects", "admin", "populated", "web/src/routes/**"))
	if err := db.RecordCapture(ctx, store.PageCapture{ProjectID: "PRJ-1",
		PageKey: manifest.Pages[0].Key(), Route: "/projects", Role: "admin", State: "populated",
		CommitSHA: taken, ArtifactPath: "screenshots/projects.png"}); err != nil {
		t.Fatal(err)
	}
	// A commit that touches nothing under this screen.
	head := commitFile(t, repo, "internal/store/x.go", "unrelated")
	report, err := Status(ctx, db, repo, "PRJ-1", head, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if report.Statuses[0].Status != StatusCurrent {
		t.Fatalf("nothing under this screen moved: %+v", report.Statuses[0])
	}
	if !report.Complete() {
		t.Fatal("the manifest is satisfied")
	}
	// A commit that does touch it.
	head = commitFile(t, repo, "web/src/routes/projects.tsx", "v2")
	if report, err = Status(ctx, db, repo, "PRJ-1", head, manifest); err != nil {
		t.Fatal(err)
	}
	if report.Statuses[0].Status != StatusStale {
		t.Fatalf("the screen moved under the picture: %+v", report.Statuses[0])
	}
	if !strings.Contains(report.Statuses[0].Reason, "projects.tsx") {
		t.Fatalf("the reason must name what moved: %q", report.Statuses[0].Reason)
	}
	if report.Complete() {
		t.Fatal("a stale required page leaves the manifest unsatisfied")
	}
}

// A page nobody captured is reported, not omitted. A report listing only what
// was taken reads as full coverage of a manifest nobody finished.
func TestUncapturedPagesAreReported(t *testing.T) {
	ctx, db, repo := captureFixture(t)
	head := commitFile(t, repo, "web/src/routes/projects.tsx", "v1")
	manifest := manifestOf(
		page("/projects", "admin", "populated", "web/src/routes/**"),
		page("/projects", "user", "empty", "web/src/routes/**"),
	)
	report, err := Status(ctx, db, repo, "PRJ-1", head, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if report.Missing != 2 || len(report.Statuses) != 2 {
		t.Fatalf("report=%+v", report)
	}
	if report.Complete() {
		t.Fatal("nothing has been captured")
	}
	short := report.Shortfall()
	if len(short) != 2 || !strings.Contains(short[0], "MISSING") {
		t.Fatalf("shortfall=%v", short)
	}
}

// The same route shows different things to different people and in different
// states. Treating them as one page documents a product most readers do not
// have.
func TestTheSameRouteInAnotherRoleIsAnotherPage(t *testing.T) {
	ctx, db, repo := captureFixture(t)
	head := commitFile(t, repo, "web/src/routes/projects.tsx", "v1")
	admin := page("/projects", "admin", "populated", "web/src/routes/**")
	user := page("/projects", "user", "populated", "web/src/routes/**")
	empty := page("/projects", "admin", "empty", "web/src/routes/**")
	if admin.Key() == user.Key() || admin.Key() == empty.Key() {
		t.Fatal("role and state are part of which screen this is")
	}
	if err := db.RecordCapture(ctx, store.PageCapture{ProjectID: "PRJ-1", PageKey: admin.Key(),
		Route: "/projects", Role: "admin", State: "populated", CommitSHA: head,
		ArtifactPath: "a.png"}); err != nil {
		t.Fatal(err)
	}
	report, err := Status(ctx, db, repo, "PRJ-1", head, manifestOf(admin, user, empty))
	if err != nil {
		t.Fatal(err)
	}
	if report.Current != 1 || report.Missing != 2 {
		t.Fatalf("report=%+v", report)
	}
}

// A capture with no image is not a capture. Recording it would put a page in
// the done column with nothing behind it.
func TestACaptureWithNoImageIsRefused(t *testing.T) {
	ctx, db, _ := captureFixture(t)
	err := db.RecordCapture(ctx, store.PageCapture{ProjectID: "PRJ-1", PageKey: "k",
		CommitSHA: "abc123", ArtifactPath: "  "})
	if err == nil {
		t.Fatal("a capture with no image must be refused")
	}
	if err = db.RecordCapture(ctx, store.PageCapture{ProjectID: "PRJ-1", PageKey: "k",
		ArtifactPath: "a.png"}); err == nil {
		t.Fatal("a capture that does not say what it depicts must be refused")
	}
}

// A manifest that cannot be executed is refused at the point it is written,
// not at the point somebody runs it and finds out.
func TestAManifestThatCannotBeExecutedIsRefused(t *testing.T) {
	good := manifestOf(page("/projects", "admin", "populated", "web/**"))
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	unreachable := good
	unreachable.Pages = []Page{{Route: "/x", Role: "admin", State: "populated", Sources: []string{"web/**"}}}
	if err := unreachable.Validate(); err == nil {
		t.Fatal("a page with no script cannot be captured")
	}
	sourceless := good
	sourceless.Pages = []Page{{Route: "/x", Role: "admin", State: "populated", Script: "s"}}
	if err := sourceless.Validate(); err == nil {
		t.Fatal("a page with no sources can never be shown to be stale")
	}
	noSeed := good
	noSeed.SeedRef = ""
	if err := noSeed.Validate(); err == nil {
		t.Fatal("without a seed nobody can say whether the names in the screenshots are real")
	}
	duplicate := good
	duplicate.Pages = []Page{page("/x", "a", "b", "web/**"), page("/x", "a", "b", "web/**")}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("the same screen twice is a mistake, not two screens")
	}
}

// Stale and missing come before current, because what needs doing is what the
// reader opened the report for.
func TestWhatNeedsDoingIsListedFirst(t *testing.T) {
	ctx, db, repo := captureFixture(t)
	commitFile(t, repo, "web/a.tsx", "v1")
	taken := commitFile(t, repo, "web/b.tsx", "v1")
	current := page("/current", "admin", "populated", "docs/**")
	stale := page("/stale", "admin", "populated", "web/**")
	missing := page("/missing", "admin", "populated", "web/**")
	for _, p := range []Page{current, stale} {
		if err := db.RecordCapture(ctx, store.PageCapture{ProjectID: "PRJ-1", PageKey: p.Key(),
			Route: p.Route, Role: p.Role, State: p.State, CommitSHA: taken, ArtifactPath: "x.png"}); err != nil {
			t.Fatal(err)
		}
	}
	head := commitFile(t, repo, "web/a.tsx", "v2")
	report, err := Status(ctx, db, repo, "PRJ-1", head, manifestOf(current, stale, missing))
	if err != nil {
		t.Fatal(err)
	}
	if report.Statuses[0].Status != StatusStale || report.Statuses[1].Status != StatusMissing {
		t.Fatalf("order=%v %v %v", report.Statuses[0].Status, report.Statuses[1].Status, report.Statuses[2].Status)
	}
	if report.Current != 1 || report.Stale != 1 || report.Missing != 1 {
		t.Fatalf("report=%+v", report)
	}
}
