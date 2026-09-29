package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ListTree is every file in the repository at one commit.
//
// It reads a commit rather than the working tree on purpose. A working-tree
// read cannot be reproduced — the next reader sees a different repository —
// and it includes files nobody committed, so an assessment could pass on a
// change that exists only on one machine.
func ListTree(ctx context.Context, repository, sha string) ([]string, error) {
	if repository == "" {
		return nil, errors.New("repository is required")
	}
	if !commitSHA.MatchString(sha) {
		return nil, fmt.Errorf("%q is not a commit SHA", sha)
	}
	output, err := exec.CommandContext(ctx, "git", "-C", repository, "ls-tree", "-r", "--name-only", sha).Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree %s: %w", abbreviate(sha), err)
	}
	var paths []string
	for _, line := range strings.Split(string(output), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// ErrPathNotInTree means the file is not present at that commit. It is
// distinguished from a read failure because an absence is a finding and a
// failure is not.
var ErrPathNotInTree = errors.New("path is not in the tree at that commit")

// FileAt reads one file as it was at a commit.
//
// maxBytes caps the read: a repository can hold a two hundred megabyte fixture,
// and a detector looking for an import statement does not need it.
func FileAt(ctx context.Context, repository, sha, path string, maxBytes int) (string, error) {
	if repository == "" || strings.TrimSpace(path) == "" {
		return "", errors.New("repository and path are required")
	}
	if !commitSHA.MatchString(sha) {
		return "", fmt.Errorf("%q is not a commit SHA", sha)
	}
	if strings.HasPrefix(path, "-") {
		// A path that looks like an option is not a path. git would read it as
		// one and do something nobody asked for.
		return "", fmt.Errorf("%q is not a path", path)
	}
	if maxBytes <= 0 {
		maxBytes = 512 * 1024
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repository, "show", sha+":"+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "does not exist") || strings.Contains(stderr.String(), "exists on disk") {
			return "", fmt.Errorf("%s: %w", path, ErrPathNotInTree)
		}
		return "", fmt.Errorf("git show %s:%s: %w", abbreviate(sha), path, err)
	}
	if len(output) > maxBytes {
		output = output[:maxBytes]
	}
	return strings.ToValidUTF8(string(output), ""), nil
}
