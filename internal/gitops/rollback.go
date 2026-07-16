package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func RollbackWorktree(ctx context.Context, worktree Worktree, changes []FileChange) error {
	if worktree.Path == "" || worktree.Branch == "" || worktree.BaseCommit == "" {
		return errors.New("worktree path, branch, and target commit are required")
	}
	branch, err := gitOutput(ctx, worktree.Path, "branch", "--show-current")
	if err != nil {
		return err
	}
	if branch != worktree.Branch {
		return fmt.Errorf("worktree branch changed: expected %s current %s", worktree.Branch, branch)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", worktree.Path, "reset", "--hard", worktree.BaseCommit)
	if output, resetErr := cmd.CombinedOutput(); resetErr != nil {
		return fmt.Errorf("reset worktree: %w: %s", resetErr, strings.TrimSpace(string(output)))
	}
	for _, change := range changes {
		if change.ChangeType != "ADDED" {
			continue
		}
		path := filepath.Clean(filepath.Join(worktree.Path, filepath.FromSlash(change.Path)))
		relative, relErr := filepath.Rel(worktree.Path, path)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe rollback path %q", change.Path)
		}
		if err = os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove added path %s: %w", change.Path, err)
		}
	}
	return nil
}
