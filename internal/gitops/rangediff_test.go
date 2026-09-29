package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func diffRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	return dir
}

func commit(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "c " + name}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// A reviewer who approved one commit and is being asked to look again needs the
// part that changed since, not the whole change over again. Re-reading a
// thousand lines to find the twenty that moved is how a re-review becomes a
// rubber stamp.
func TestRangeDiffShowsOnlyWhatChangedSinceApproval(t *testing.T) {
	dir := diffRepo(t)
	commit(t, dir, "base.txt", "base\n")
	approved := commit(t, dir, "reviewed.txt", "the part already reviewed\n")
	latest := commit(t, dir, "added.txt", "the part nobody has seen\n")
	diff, truncated, err := RangeDiff(context.Background(), dir, approved, latest, 200000)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("a three-line diff is not truncated")
	}
	if !strings.Contains(diff, "added.txt") {
		t.Fatalf("the new file must be in the delta:\n%s", diff)
	}
	if strings.Contains(diff, "the part already reviewed") {
		t.Fatalf("what was already approved must not be shown again:\n%s", diff)
	}
}

// Both ends must be real commits. A range against something that is not a
// commit would otherwise be handed to git, and git's error is not one a
// reviewer can act on.
func TestRangeDiffRefusesSomethingThatIsNotACommit(t *testing.T) {
	dir := diffRepo(t)
	sha := commit(t, dir, "a.txt", "a\n")
	for _, bad := range []string{"", "HEAD", "--output=/tmp/pwned", "main"} {
		if _, _, err := RangeDiff(context.Background(), dir, sha, bad, 0); err == nil {
			t.Fatalf("%q is not a commit SHA and must be refused", bad)
		}
		if _, _, err := RangeDiff(context.Background(), dir, bad, sha, 0); err == nil {
			t.Fatalf("%q is not a commit SHA and must be refused", bad)
		}
	}
}

// A commit that is no longer reachable — the branch was rebuilt, the object
// pruned — must produce an explanation rather than an empty delta that reads
// as "nothing changed".
func TestRangeDiffAgainstAMissingCommitIsAnError(t *testing.T) {
	dir := diffRepo(t)
	sha := commit(t, dir, "a.txt", "a\n")
	gone := "0123456789abcdef0123456789abcdef01234567"
	if _, _, err := RangeDiff(context.Background(), dir, gone, sha, 0); err == nil {
		t.Fatal("a missing commit must not read as an empty delta")
	}
}
