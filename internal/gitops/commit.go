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
