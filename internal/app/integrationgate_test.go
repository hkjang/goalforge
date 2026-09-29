package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func gateFixture(t *testing.T) (context.Context, *store.Store, model.Project) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	project := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: t.TempDir(),
		DefaultBranch: "main", Provider: "codex", WIPLimit: 1}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	return ctx, db, project
}

func mergeInto(project model.Project, workID, sha string) store.ExternalEffect {
	return store.ExternalEffect{ProjectID: project.ID, WorkItemID: workID, Kind: store.EffectMergeBranch,
		Target: project.DefaultBranch, Branch: "work/" + workID, RequestHash: sha,
		Key: store.EffectKey(store.EffectMergeBranch, project.ID, workID, project.DefaultBranch, sha)}
}

// The defect this file exists for. A merge leaves the default branch
// unverified: each item was verified in its own worktree and nothing has
// checked the combination. Stacking a second merge on top of a branch already
// known to be broken buries which change broke it and makes the rollback two
// changes deep instead of one.
func TestMergingOntoAKnownBrokenBranchIsRefused(t *testing.T) {
	ctx, db, project := gateFixture(t)
	if err := db.RecordIntegrationResult(ctx, project.ID, "sha-a", "go build ./... failed", false); err != nil {
		t.Fatal(err)
	}
	err := GuardEffect(ctx, db, project, mergeInto(project, "WORK-2", "sha-b"))
	if !errors.Is(err, ErrIntegrationBroken) {
		t.Fatalf("a second merge onto a broken branch must be refused: %v", err)
	}
	if !strings.Contains(err.Error(), "go build") {
		t.Fatalf("the refusal must carry what actually broke: %v", err)
	}
	// And nothing may be recorded as intended: a refused merge that still
	// claimed the intent would make the next attempt reconcile a merge that
	// was never tried.
	if _, lookupErr := db.EffectByKey(ctx, mergeInto(project, "WORK-2", "sha-b").Key); !errors.Is(lookupErr, store.ErrNotFound) {
		t.Fatalf("a refused merge must not leave an intent behind: %v", lookupErr)
	}
}

// Verification that has not run yet is a different thing from verification
// that failed. A branch merged five minutes ago has not been checked, and
// refusing every subsequent merge until someone runs the check would stop the
// ordinary flow the product is built around.
func TestAnUncheckedBranchDoesNotBlockTheNextMerge(t *testing.T) {
	ctx, db, project := gateFixture(t)
	if err := db.MarkIntegrationPending(ctx, project.ID, "병합 후 통합 검증이 필요합니다", "sha-a"); err != nil {
		t.Fatal(err)
	}
	if err := GuardEffect(ctx, db, project, mergeInto(project, "WORK-2", "sha-b")); err != nil {
		t.Fatalf("not yet verified is not the same as known broken: %v", err)
	}
}

// Once the branch is verified again, merging resumes.
func TestMergingResumesAfterTheBranchIsFixed(t *testing.T) {
	ctx, db, project := gateFixture(t)
	if err := db.RecordIntegrationResult(ctx, project.ID, "sha-a", "broken", false); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordIntegrationResult(ctx, project.ID, "sha-a2", "", true); err != nil {
		t.Fatal(err)
	}
	if err := GuardEffect(ctx, db, project, mergeInto(project, "WORK-2", "sha-b")); err != nil {
		t.Fatalf("a repaired branch accepts merges again: %v", err)
	}
}

// The gate is about merging, not about everything. Publishing a branch to a
// remote does not touch the default branch, and blocking it would stop the one
// action that lets someone else look at the fix.
func TestPublishingIsNotBlockedByABrokenBranch(t *testing.T) {
	ctx, db, project := gateFixture(t)
	if err := db.RecordIntegrationResult(ctx, project.ID, "sha-a", "broken", false); err != nil {
		t.Fatal(err)
	}
	effect := store.ExternalEffect{ProjectID: project.ID, WorkItemID: "WORK-2", Kind: store.EffectPublishBranch,
		Target: "origin", Branch: "work/WORK-2", RequestHash: "sha-b",
		Key: store.EffectKey(store.EffectPublishBranch, project.ID, "WORK-2", "origin", "sha-b")}
	if err := GuardEffect(ctx, db, project, effect); err != nil {
		t.Fatalf("publishing a branch does not touch the default branch: %v", err)
	}
}

// The integration repair item is how the branch gets fixed, so the fix itself
// must be mergeable. A gate that blocks its own remedy is a deadlock.
func TestTheIntegrationRepairItselfMayMerge(t *testing.T) {
	ctx, db, project := gateFixture(t)
	if err := db.RecordIntegrationResult(ctx, project.ID, "sha-a", "broken", false); err != nil {
		t.Fatal(err)
	}
	goal, err := db.SetGoal(ctx, project.ID, "G", "o", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	repair, _, err := db.RecordIntegrationFailure(ctx, project.ID, goal.ID, "sha-a", []string{"build"})
	if err != nil {
		t.Fatal(err)
	}
	if err := GuardEffect(ctx, db, project, mergeInto(project, repair.ID, "sha-fix")); err != nil {
		t.Fatalf("the repair for the breakage must be mergeable: %v", err)
	}
}
