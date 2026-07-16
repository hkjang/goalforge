package gitops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// CommitInfo describes a commit created for a verified run.
type CommitInfo struct {
	CommitSHA, Branch string
	FilesCommitted    int
}

const (
	commitAuthorName  = "GoalForge"
	commitAuthorEmail = "goalforge@goalforge.invalid"
)

// ErrMergeConflict means the merge could not complete cleanly; the merge was
// aborted and the repository left untouched for user review (자동 병합 금지).
var ErrMergeConflict = errors.New("merge conflict requires user review")

// MergeVerified merges a verified work branch into defaultBranch with a
// traceable merge commit. The repository must already be checked out on
// defaultBranch with a clean tree; a conflicting merge is aborted and
// surfaced as ErrMergeConflict instead of being auto-resolved.
func MergeVerified(ctx context.Context, repository, defaultBranch, branch, message string) (string, error) {
	if repository == "" || defaultBranch == "" || branch == "" || message == "" {
		return "", errors.New("repository, default branch, work branch, and message are required")
	}
	current, err := gitOutput(ctx, repository, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	if current != defaultBranch {
		return "", fmt.Errorf("repository is on %s; check out %s before merging", current, defaultBranch)
	}
	status, err := gitOutput(ctx, repository, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", errors.New("working tree must be clean before merging")
	}
	merge := exec.CommandContext(ctx, "git",
		"-C", repository,
		"-c", "user.name="+commitAuthorName,
		"-c", "user.email="+commitAuthorEmail,
		"merge", "--no-ff", "-m", message, branch)
	if output, mergeErr := merge.CombinedOutput(); mergeErr != nil {
		abort := exec.CommandContext(ctx, "git", "-C", repository, "merge", "--abort")
		_ = abort.Run()
		return "", fmt.Errorf("%w: %s", ErrMergeConflict, strings.TrimSpace(string(output)))
	}
	return gitOutput(ctx, repository, "rev-parse", "HEAD")
}

// PushBranch publishes branch to the named remote. Callers must hold an
// explicit user approval first (SEC-011); this function never forces.
func PushBranch(ctx context.Context, repository, remote, branch string) error {
	if repository == "" || remote == "" || branch == "" {
		return errors.New("repository, remote, and branch are required")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repository, "push", remote, branch)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("push %s to %s: %w: %s", branch, remote, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// CommitVerified stages every pending change in repository and commits it with
// Goal-ID, Work-Item-ID, and Run-ID trailers so AI changes stay attributable.
// It refuses to commit on protectedBranch and returns an empty CommitInfo when
// the tree is already clean. Callers must only invoke it after verification
// gates pass; committing unverified work is a policy violation.
func CommitVerified(ctx context.Context, repository, protectedBranch, goalID, workItemID, runID, title string) (CommitInfo, error) {
	if repository == "" || goalID == "" || workItemID == "" || runID == "" {
		return CommitInfo{}, errors.New("repository, goal, work item, and run are required")
	}
	branch, err := gitOutput(ctx, repository, "branch", "--show-current")
	if err != nil {
		return CommitInfo{}, err
	}
	if branch == "" {
		return CommitInfo{}, errors.New("refusing to commit on a detached HEAD")
	}
	if protectedBranch != "" && branch == protectedBranch {
		return CommitInfo{}, fmt.Errorf("refusing to commit on protected branch %s", protectedBranch)
	}
	status, err := gitOutput(ctx, repository, "status", "--porcelain")
	if err != nil {
		return CommitInfo{}, err
	}
	if status == "" {
		return CommitInfo{}, nil
	}
	files := len(strings.Split(status, "\n"))
	if output, addErr := exec.CommandContext(ctx, "git", "-C", repository, "add", "-A").CombinedOutput(); addErr != nil {
		return CommitInfo{}, fmt.Errorf("stage changes: %w: %s", addErr, strings.TrimSpace(string(output)))
	}
	subject := strings.TrimSpace(title)
	if subject == "" {
		subject = "GoalForge verified change for " + workItemID
	}
	message := subject + "\n\nGoal-ID: " + goalID + "\nWork-Item-ID: " + workItemID + "\nRun-ID: " + runID + "\n"
	commit := exec.CommandContext(ctx, "git",
		"-C", repository,
		"-c", "user.name="+commitAuthorName,
		"-c", "user.email="+commitAuthorEmail,
		"commit", "-m", message)
	if output, commitErr := commit.CombinedOutput(); commitErr != nil {
		return CommitInfo{}, fmt.Errorf("commit verified change: %w: %s", commitErr, strings.TrimSpace(string(output)))
	}
	sha, err := gitOutput(ctx, repository, "rev-parse", "HEAD")
	if err != nil {
		return CommitInfo{}, err
	}
	return CommitInfo{CommitSHA: sha, Branch: branch, FilesCommitted: files}, nil
}
