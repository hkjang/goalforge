package gitops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var commitSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// CommitDiff returns a commit's stat summary and patch so a change can be
// reviewed next to its verification evidence instead of in a separate terminal.
// The SHA is matched against a hex pattern before it reaches git, and the
// output is truncated rather than streamed unbounded into a response.
func CommitDiff(ctx context.Context, repository, sha string, maxBytes int) (string, bool, error) {
	if repository == "" || sha == "" {
		return "", false, errors.New("repository and commit SHA are required")
	}
	if !commitSHA.MatchString(sha) {
		return "", false, fmt.Errorf("%q is not a commit SHA", sha)
	}
	if maxBytes <= 0 {
		maxBytes = 200000
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repository, "show", "--stat", "--patch", "--no-color", "--find-renames", sha)
	output, err := cmd.Output()
	if err != nil {
		return "", false, fmt.Errorf("git show %s: %w", sha, err)
	}
	diff := string(output)
	if len(diff) > maxBytes {
		return strings.ToValidUTF8(diff[:maxBytes], ""), true, nil
	}
	return strings.ToValidUTF8(diff, ""), false, nil
}

// ChangedSince lists the files that differ between a commit and the current
// HEAD. It is how a record that pinned the code it was reasoning about can be
// asked whether that code still looks the way it did.
//
// An unknown commit returns ErrUnknownCommit rather than an empty list: "no
// files changed" and "I cannot tell" are different answers, and returning the
// first for the second would quietly mark stale reasoning as current.
func ChangedSince(ctx context.Context, repository, commit string) ([]string, error) {
	if strings.TrimSpace(commit) == "" {
		return nil, ErrUnknownCommit
	}
	if err := exec.CommandContext(ctx, "git", "-C", repository, "cat-file", "-e", commit+"^{commit}").Run(); err != nil {
		return nil, ErrUnknownCommit
	}
	output, err := exec.CommandContext(ctx, "git", "-C", repository, "diff", "--name-only", commit, "HEAD").Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(string(output), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			files = append(files, trimmed)
		}
	}
	return files, nil
}

// ErrUnknownCommit means the recorded baseline is not in this repository, so
// nothing can be said about what changed since.
var ErrUnknownCommit = errors.New("commit is not in this repository")

// RangeDiff is what changed between two commits.
//
// It exists for re-review. When the work item is re-run after someone approved
// it, the approval screen used to show the diff of the commit that was already
// approved while telling the reader to review the new change — the screen said
// one thing and displayed another. What a reviewer needs is the part that moved
// since they last looked; re-reading a thousand lines to find the twenty that
// changed is how a re-review becomes a rubber stamp.
func RangeDiff(ctx context.Context, repository, from, to string, maxBytes int) (string, bool, error) {
	if repository == "" {
		return "", false, errors.New("repository is required")
	}
	// Both ends are validated as commit SHAs rather than passed to git as
	// written. A ref name would work by luck and an option-shaped string would
	// not be an argument at all.
	for _, sha := range []string{from, to} {
		if !commitSHA.MatchString(sha) {
			return "", false, fmt.Errorf("%q is not a commit SHA", sha)
		}
	}
	if maxBytes <= 0 {
		maxBytes = 200000
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repository, "diff", "--stat", "--patch", "--no-color",
		"--find-renames", from, to)
	output, err := cmd.Output()
	if err != nil {
		// An unreachable commit — the branch was rebuilt, the object pruned —
		// must say so. An empty delta would read as "nothing changed", which is
		// the one wrong answer a re-review screen can give.
		return "", false, fmt.Errorf("git diff %s..%s: %w", abbreviate(from), abbreviate(to), err)
	}
	diff := string(output)
	if len(diff) > maxBytes {
		return strings.ToValidUTF8(diff[:maxBytes], ""), true, nil
	}
	return strings.ToValidUTF8(diff, ""), false, nil
}

// abbreviate shortens a SHA for a message. The full forty characters in an
// error line push the part that says what went wrong off the edge.
func abbreviate(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
