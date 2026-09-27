package gitops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// RemoteBranchSHA reports what a remote currently has at a branch, and whether
// the branch exists there at all. It is how a publish whose outcome was never
// recorded is settled: the remote is the authority on whether it happened.
func RemoteBranchSHA(ctx context.Context, repository, remote, branch string) (string, bool, error) {
	if repository == "" || remote == "" || branch == "" {
		return "", false, errors.New("repository, remote, and branch are required")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repository, "ls-remote", "--heads", remote, branch)
	output, err := cmd.Output()
	if err != nil {
		return "", false, fmt.Errorf("query %s for %s: %w", remote, branch, err)
	}
	line := strings.TrimSpace(string(output))
	if line == "" {
		return "", false, nil
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false, nil
	}
	return fields[0], true, nil
}

// CommitIsAncestor reports whether commit is already contained in ref. It
// settles a merge whose outcome was never recorded: if the commit is in the
// branch, the merge happened.
func CommitIsAncestor(ctx context.Context, repository, commit, ref string) (bool, error) {
	if repository == "" || commit == "" || ref == "" {
		return false, errors.New("repository, commit, and ref are required")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repository, "merge-base", "--is-ancestor", commit, ref)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	// Exit status 1 is the documented "not an ancestor" answer; anything else
	// means the question could not be answered, which must not be reported as
	// a confident "no".
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether %s is in %s: %w", commit, ref, err)
}
