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

// ErrWorktreeMissing means a worktree GoalForge recorded is no longer on this
// filesystem.
//
// It happens more than it looks like it should: `git worktree prune`, a manual
// delete, a fresh clone, a disk that was reset — and, once a PostgreSQL queue
// is shared, a worker on a different machine picking up work whose worktree
// was created somewhere else. Without a name for it the failure surfaces as
// `fatal: cannot change to '...'`, which reads like the repository is broken.
var ErrWorktreeMissing = errors.New("recorded worktree is no longer present")

// ErrWorktreeDiverged means the path exists but holds a different branch than
// the one recorded, so it is not the work that was left there.
var ErrWorktreeDiverged = errors.New("worktree holds a different branch than the one recorded")

// WorktreeIntact reports whether a recorded worktree is still the one that was
// recorded. It answers before anything tries to read the tree, so the failure
// can say what happened instead of quoting git.
func WorktreeIntact(ctx context.Context, worktree Worktree) error {
	if strings.TrimSpace(worktree.Path) == "" {
		return fmt.Errorf("%w: no path recorded", ErrWorktreeMissing)
	}
	info, err := os.Stat(worktree.Path)
	if os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrWorktreeMissing, worktree.Path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrWorktreeMissing, worktree.Path)
	}
	branch, err := gitOutput(ctx, worktree.Path, "branch", "--show-current")
	if err != nil {
		// The directory survived but git no longer recognises it — a pruned
		// worktree leaves the files and removes the link.
		return fmt.Errorf("%w: %s (git 가 더 이상 이 경로를 워크트리로 인식하지 않습니다)", ErrWorktreeMissing, worktree.Path)
	}
	if worktree.Branch != "" && branch != worktree.Branch {
		return fmt.Errorf("%w: %s 에 %s 가 있어야 하는데 %s 가 있습니다", ErrWorktreeDiverged, worktree.Path, worktree.Branch, branch)
	}
	return nil
}

// ExplainMissingWorktree says what survived and what did not, because the
// answer decides what the operator should do next.
//
// The committed work is safe: the branch still exists in the main repository
// and the worktree can be made again from it. Uncommitted work in the deleted
// directory is gone, and no phrasing changes that — saying so plainly is
// better than a recovery that silently continues on a tree missing the changes
// the checkpoint recorded.
func ExplainMissingWorktree(repository string, worktree Worktree) string {
	return fmt.Sprintf("기록된 워크트리 %s 가 없습니다. 커밋된 작업은 브랜치 %s 에 그대로 남아 있습니다 — "+
		"git -C %s worktree add %s %s 로 되살릴 수 있습니다. "+
		"커밋되지 않은 변경은 복구할 수 없습니다. "+
		"PostgreSQL 큐를 공유한다면 이 워크트리는 다른 기계에서 만들어졌을 수 있습니다.",
		worktree.Path, worktree.Branch, repository, worktree.Path, worktree.Branch)
}
