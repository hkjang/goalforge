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
