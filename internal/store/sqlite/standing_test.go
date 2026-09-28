package sqlite

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo builds a real repository, because whether a decision's code has
// moved is a question only git can answer and a fake would prove nothing.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	run("init", "-q")
	write(t, root, "internal/session/store.go", "package session\n")
	write(t, root, "docs/README.md", "docs\n")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return root
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, root, message string) string {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", message}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	return head(t, root)
}

func head(t *testing.T, root string) string {
	t.Helper()
	output, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}

// The defect this closes: a decision recorded a base commit and nothing ever
// compared it to anything, so a note about a module that has since been
// rewritten kept being handed to every new session as current fact.
func TestDecisionNeedsReviewWhenItsCodeHasMoved(t *testing.T) {
	ctx := context.Background()
	root := gitRepo(t)
	base := head(t, root)
	decisions := []DesignDecision{
		{ID: "DEC-1", Title: "세션 저장소는 SQLite", BaseCommit: base, Scope: "internal/session/**"},
		{ID: "DEC-2", Title: "문서는 마크다운", BaseCommit: base, Scope: "docs/**"},
	}
	// Nothing has changed yet, so both stand.
	for _, standing := range DecisionStandings(ctx, root, decisions) {
		if standing.Standing != StandingCurrent {
			t.Fatalf("%s should stand: %+v", standing.Decision.ID, standing)
		}
	}
	// Rewrite the code one decision was about.
	write(t, root, "internal/session/store.go", "package session\n\n// rewritten\n")
	commitAll(t, root, "rewrite session store")
	standings := DecisionStandings(ctx, root, decisions)
	if standings[0].Standing != StandingReviewNeeded {
		t.Fatalf("a decision whose files moved must be flagged: %+v", standings[0])
	}
	if !strings.Contains(standings[0].Detail, "internal/session/store.go") {
		t.Fatalf("the detail must name what changed: %q", standings[0].Detail)
	}
	// The other decision is about untouched files and must not be dragged down
	// with it, or every commit would invalidate every decision and the signal
	// would be worthless.
	if standings[1].Standing != StandingCurrent {
		t.Fatalf("an unrelated decision must not be flagged: %+v", standings[1])
	}
}

// A decision about the project as a whole is unsettled by any change, which is
// the honest reading of "no scope recorded".
func TestScopelessDecisionIsUnsettledByAnyChange(t *testing.T) {
	ctx := context.Background()
	root := gitRepo(t)
	base := head(t, root)
	write(t, root, "docs/README.md", "docs changed\n")
	commitAll(t, root, "touch docs")
	standings := DecisionStandings(ctx, root, []DesignDecision{{ID: "DEC-1", BaseCommit: base}})
	if standings[0].Standing != StandingReviewNeeded {
		t.Fatalf("%+v", standings[0])
	}
}

// "I cannot tell" must never be reported as "still current". That silent
// promotion is what let stale reasoning through in the first place.
func TestUnjudgeableDecisionsAreNotCalledCurrent(t *testing.T) {
	ctx := context.Background()
	root := gitRepo(t)
	for name, decision := range map[string]DesignDecision{
		"no baseline recorded": {ID: "DEC-1"},
		"baseline not in this repository": {ID: "DEC-2",
			BaseCommit: "0000000000000000000000000000000000000000"},
	} {
		t.Run(name, func(t *testing.T) {
			standings := DecisionStandings(ctx, root, []DesignDecision{decision})
			if standings[0].Standing != StandingUnanchored {
				t.Fatalf("%+v", standings[0])
			}
			if !standings[0].NeedsReview() {
				t.Fatal("an unjudgeable decision must not be relied on as current")
			}
			if standings[0].Detail == "" {
				t.Fatal("the user must be told why it cannot be judged")
			}
		})
	}
}

// Many decisions usually share one baseline; the diff for it is computed once.
func TestOneBaselineIsDiffedOnce(t *testing.T) {
	ctx := context.Background()
	root := gitRepo(t)
	base := head(t, root)
	decisions := make([]DesignDecision, 50)
	for i := range decisions {
		decisions[i] = DesignDecision{ID: "DEC", BaseCommit: base, Scope: "internal/**"}
	}
	// Correctness is what the test can assert; the caching is what makes
	// assembling context for a project with a long decision log affordable.
	for _, standing := range DecisionStandings(ctx, root, decisions) {
		if standing.Standing != StandingCurrent {
			t.Fatalf("%+v", standing)
		}
	}
}
