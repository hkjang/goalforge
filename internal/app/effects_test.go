package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func effectFixture(t *testing.T) (context.Context, *store.Store, model.Project, string, string) {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	remote := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Skipf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run(remote, "init", "--bare", "-q", "-b", "main")
	run(repo, "init", "-q", "-b", "main")
	run(repo, "config", "user.email", "t@example.invalid")
	run(repo, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-qm", "base")
	run(repo, "remote", "add", "origin", remote)
	run(repo, "checkout", "-q", "-b", "goalforge/W1")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-qm", "feature")
	head := run(repo, "rev-parse", "HEAD")
	run(repo, "checkout", "-q", "main")

	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: repo, DefaultBranch: "main", Provider: "codex"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	return ctx, db, project, head, remote
}

// AT-05: the push reached the remote and the local record never got written.
// A retry has to ask the remote rather than push again, and report that the
// change is already applied instead of making a second one.
func TestPublishReconcilesInsteadOfRepeating(t *testing.T) {
	ctx, db, project, head, remote := effectFixture(t)
	effect := store.ExternalEffect{ProjectID: project.ID, WorkItemID: "W1", Kind: store.EffectPublishBranch,
		Target: "origin", Branch: "goalforge/W1", RequestHash: head,
		Key: store.EffectKey(store.EffectPublishBranch, project.ID, "W1", "origin", head)}
	// The intent was recorded, the push happened, and the process died before
	// the outcome was written.
	if _, _, err := db.BeginEffect(ctx, effect); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", project.RepositoryPath, "push", "-q", "origin", "goalforge/W1").CombinedOutput(); err != nil {
		t.Fatalf("push: %v %s", err, output)
	}
	if err := db.SettleEffect(ctx, effect.Key, store.EffectUnknown, "worker died after pushing"); err != nil {
		t.Fatal(err)
	}
	err := GuardEffect(ctx, db, project, effect)
	if !errors.Is(err, ErrEffectAlreadyApplied) {
		t.Fatalf("a retry must detect the remote already has it: %v", err)
	}
	settled, err := db.EffectByKey(ctx, effect.Key)
	if err != nil || settled.State != store.EffectSucceeded {
		t.Fatalf("reconciliation must settle the ledger: %+v err=%v", settled, err)
	}
	// The remote has exactly one branch at that commit; nothing was duplicated.
	output, err := exec.Command("git", "-C", remote, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(output), "goalforge/W1") != 1 {
		t.Fatalf("remote refs: %s", output)
	}
}

// When the effect never reached the remote, the retry is allowed and counted
// rather than refused.
func TestGuardAllowsRetryWhenNothingWasApplied(t *testing.T) {
	ctx, db, project, head, _ := effectFixture(t)
	effect := store.ExternalEffect{ProjectID: project.ID, WorkItemID: "W1", Kind: store.EffectPublishBranch,
		Target: "origin", Branch: "goalforge/W1", RequestHash: head,
		Key: store.EffectKey(store.EffectPublishBranch, project.ID, "W1", "origin", head)}
	if _, _, err := db.BeginEffect(ctx, effect); err != nil {
		t.Fatal(err)
	}
	if err := db.SettleEffect(ctx, effect.Key, store.EffectUnknown, "network dropped"); err != nil {
		t.Fatal(err)
	}
	if err := GuardEffect(ctx, db, project, effect); err != nil {
		t.Fatalf("an effect that never landed must be retryable: %v", err)
	}
	reopened, err := db.EffectByKey(ctx, effect.Key)
	if err != nil || reopened.State != store.EffectIntended || reopened.Attempts != 2 {
		t.Fatalf("the retry must be counted: %+v err=%v", reopened, err)
	}
}

// A merge whose outcome was never recorded is settled by asking whether the
// commit is already in the branch.
func TestMergeReconciliationUsesTheBranchContents(t *testing.T) {
	ctx, db, project, head, _ := effectFixture(t)
	effect := store.ExternalEffect{ProjectID: project.ID, WorkItemID: "W1", Kind: store.EffectMergeBranch,
		Target: "main", Branch: "goalforge/W1", RequestHash: head,
		Key: store.EffectKey(store.EffectMergeBranch, project.ID, "W1", "main", head)}
	if _, _, err := db.BeginEffect(ctx, effect); err != nil {
		t.Fatal(err)
	}
	if err := db.SettleEffect(ctx, effect.Key, store.EffectUnknown, "died mid-merge"); err != nil {
		t.Fatal(err)
	}
	// Not merged yet: the retry proceeds.
	if err := GuardEffect(ctx, db, project, effect); err != nil {
		t.Fatalf("an unmerged commit must be retryable: %v", err)
	}
	if output, err := exec.Command("git", "-C", project.RepositoryPath, "merge", "-q", "--no-ff", "-m", "merge", "goalforge/W1").CombinedOutput(); err != nil {
		t.Fatalf("merge: %v %s", err, output)
	}
	if err := db.SettleEffect(ctx, effect.Key, store.EffectUnknown, "died after merging"); err != nil {
		t.Fatal(err)
	}
	if err := GuardEffect(ctx, db, project, effect); !errors.Is(err, ErrEffectAlreadyApplied) {
		t.Fatalf("a merged commit must be detected: %v", err)
	}
}

// An outcome that cannot be determined is not guessed at: nothing is retried
// until it is settled, because retrying something that may have happened is
// the duplicate this ledger exists to prevent.
func TestUnreachableRemoteLeavesTheEffectUnresolved(t *testing.T) {
	ctx, db, project, head, _ := effectFixture(t)
	effect := store.ExternalEffect{ProjectID: project.ID, WorkItemID: "W1", Kind: store.EffectPublishBranch,
		Target: "no-such-remote", Branch: "goalforge/W1", RequestHash: head,
		Key: store.EffectKey(store.EffectPublishBranch, project.ID, "W1", "no-such-remote", head)}
	if _, _, err := db.BeginEffect(ctx, effect); err != nil {
		t.Fatal(err)
	}
	if err := db.SettleEffect(ctx, effect.Key, store.EffectUnknown, "died"); err != nil {
		t.Fatal(err)
	}
	err := GuardEffect(ctx, db, project, effect)
	if !errors.Is(err, ErrEffectUnresolved) {
		t.Fatalf("an unanswerable question must block the retry: %v", err)
	}
	unresolved, err := db.UnresolvedEffects(ctx, project.ID)
	if err != nil || len(unresolved) != 1 {
		t.Fatalf("it must stay on the unresolved list: %+v err=%v", unresolved, err)
	}
}
