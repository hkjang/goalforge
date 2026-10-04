package verification

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/policy"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/testscript"
)

// The classification needs the module path, and it reaches it only if the
// engine reads and passes it. Without that the distinction exists in policy and
// never fires: a package of this repository goes on being reported as a
// registry problem, with RepairEnvironment stopping the loop from writing it.
func TestTheEngineTellsTheClassifierWhichPackagesAreLocal(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine, err := New(db, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	if err = os.WriteFile(filepath.Join(repository, "go.mod"),
		[]byte("module example.com/notes\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A gate whose output is what Go says about a package of this module that
	// has not been written. Produced by a script so the engine's own reading
	// of the output is what is under test.
	body := "echo 'main.go:11:2: no required module provides package example.com/notes/store; to add it:' >&2\n" +
		"echo '\tgo get example.com/notes/store' >&2\nexit 1"
	script := testscript.Write(t, repository, "build", body, body)
	results, passed, err := engine.Check(context.Background(), repository,
		[]Gate{{Type: "go_build", Command: []string{script}, Timeout: 30 * time.Second,
			Required: true, SuccessValue: "true", Kind: "build"}})
	if err != nil {
		t.Fatal(err)
	}
	if passed || len(results) != 1 {
		t.Fatalf("passed=%v results=%+v", passed, results)
	}
	if results[0].RepairMode == string(policy.RepairEnvironment) {
		t.Fatalf("no registry action fixes a package of this module: %+v", results[0])
	}
	if results[0].FailureKind != string(policy.GateMissingLocalPackage) {
		t.Fatalf("kind=%s", results[0].FailureKind)
	}
	if !strings.Contains(results[0].FailureSummary, "이 모듈의 패키지") {
		t.Fatalf("summary=%q", results[0].FailureSummary)
	}
}

// A third-party package is unchanged: fetching it is what failed, and the loop
// cannot supply it by writing code here.
func TestAThirdPartyPackageStillReportsAsADependency(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine, err := New(db, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	if err = os.WriteFile(filepath.Join(repository, "go.mod"),
		[]byte("module example.com/notes\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "echo 'main.go:5:2: no required module provides package github.com/pkg/errors; to add it:' >&2\nexit 1"
	script := testscript.Write(t, repository, "build", body, body)
	results, _, err := engine.Check(context.Background(), repository,
		[]Gate{{Type: "go_build", Command: []string{script}, Timeout: 30 * time.Second,
			Required: true, SuccessValue: "true", Kind: "build"}})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].RepairMode != string(policy.RepairEnvironment) {
		t.Fatalf("this one needs the registry: %+v", results[0])
	}
}
