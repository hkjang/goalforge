package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Worktree struct{ Path, Branch, BaseCommit string }

var unsafeBranch = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func EnsureWorktree(ctx context.Context, repository, projectID, workItemID string) (Worktree, error) {
	if repository == "" || projectID == "" || workItemID == "" {
		return Worktree{}, errors.New("repository, project, and work item are required")
	}
	base, err := gitOutput(ctx, repository, "rev-parse", "HEAD")
	if err != nil {
		return Worktree{}, err
	}
	branch := "goalforge/" + safeRef(projectID) + "-" + safeRef(workItemID)
	root := repository + ".goalforge-worktrees"
	path := filepath.Join(root, safeRef(workItemID))
	if _, statErr := os.Stat(path); statErr == nil {
		actual, branchErr := gitOutput(ctx, path, "branch", "--show-current")
		if branchErr != nil || actual != branch {
			return Worktree{}, fmt.Errorf("existing worktree does not match %s", branch)
		}
		return Worktree{Path: path, Branch: branch, BaseCommit: base}, nil
	} else if !os.IsNotExist(statErr) {
		return Worktree{}, statErr
	}
	if err = os.MkdirAll(root, 0o700); err != nil {
		return Worktree{}, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repository, "worktree", "add", "-b", branch, path, base)
	if output, commandErr := cmd.CombinedOutput(); commandErr != nil {
		return Worktree{}, fmt.Errorf("create worktree: %w: %s", commandErr, strings.TrimSpace(string(output)))
	}
	return Worktree{Path: path, Branch: branch, BaseCommit: base}, nil
}

// ErrWorktreeDirty means a worktree still holds uncommitted changes and was
// left in place; pass force to discard them.
var ErrWorktreeDirty = errors.New("worktree has uncommitted changes")

// RemoveWorktree detaches worktree from repository and prunes its metadata.
// The branch is kept so committed work stays reachable. Without force a
// dirty tree is refused with ErrWorktreeDirty.
func RemoveWorktree(ctx context.Context, repository string, worktree Worktree, force bool) error {
	if repository == "" || worktree.Path == "" || worktree.Branch == "" {
		return errors.New("repository, worktree path, and branch are required")
	}
	if _, statErr := os.Stat(worktree.Path); statErr == nil {
		branch, err := gitOutput(ctx, worktree.Path, "branch", "--show-current")
		if err != nil {
			return err
		}
		if branch != worktree.Branch {
			return fmt.Errorf("worktree branch changed: expected %s current %s", worktree.Branch, branch)
		}
		if !force {
			status, err := gitOutput(ctx, worktree.Path, "status", "--porcelain")
			if err != nil {
				return err
			}
			if status != "" {
				return fmt.Errorf("%w: %s", ErrWorktreeDirty, worktree.Path)
			}
		}
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		args = append(args, worktree.Path)
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, args...)...)
		if output, removeErr := cmd.CombinedOutput(); removeErr != nil {
			return fmt.Errorf("remove worktree: %w: %s", removeErr, strings.TrimSpace(string(output)))
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	prune := exec.CommandContext(ctx, "git", "-C", repository, "worktree", "prune")
	if output, pruneErr := prune.CombinedOutput(); pruneErr != nil {
		return fmt.Errorf("prune worktrees: %w: %s", pruneErr, strings.TrimSpace(string(output)))
	}
	return nil
}

func gitOutput(ctx context.Context, repository string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func safeRef(value string) string {
	value = strings.Trim(unsafeBranch.ReplaceAllString(value, "-"), "-.")
	if value == "" {
		return "work"
	}
	return value
}
