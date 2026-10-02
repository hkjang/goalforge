package observer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// AutoApproveMerges grants the merge approvals the envelope allows.
//
// This is the point past which a change leaves the worktree and lands on the
// branch everyone else builds from, so what it requires is narrower than what
// approving execution requires: the work has to be finished, its gates have to
// have passed on the very commit being released, and the branch it is going
// into has to be working.
//
// The approval it writes is an ordinary scoped approval. It goes through the
// same request-and-grant path a person's does, lands in the same audit chain,
// and is pinned to the same commit — so `goalforge merge` cannot tell the
// difference, and neither can `integrity verify`. Nothing about the record
// degrades because a machine made it.
func AutoApproveMerges(ctx context.Context, db *store.Store, projectID, goalID string, policy AutonomyPolicy) (AutoDecisions, error) {
	decisions := AutoDecisions{Refused: map[string]string{}}
	if !policy.Enabled {
		decisions.Detail = "자동 승인이 켜져 있지 않습니다"
		return decisions, nil
	}
	if !policy.AutoMerge {
		// Turning on automatic execution is not the same decision as turning
		// on automatic release. Someone who wanted the first must not silently
		// get the second.
		decisions.Detail = "자동 병합 승인이 켜져 있지 않습니다"
		return decisions, nil
	}
	integration, err := db.IntegrationStatus(ctx, projectID)
	if err != nil {
		return decisions, err
	}
	if integration.LastSHA != "" && !integration.LastPassed {
		// The same rule `goalforge merge` enforces, applied a step earlier so
		// nothing is approved that would then be refused at the gate.
		decisions.Detail = "기본 브랜치의 통합 검증이 실패한 상태입니다 — 먼저 복구해야 합니다"
		return decisions, nil
	}
	findings, err := db.SuppliedFindings(ctx, projectID)
	if err != nil {
		return decisions, err
	}
	claims, err := db.GateClaims(ctx, projectID)
	if err != nil {
		return decisions, err
	}
	judgedBy := map[string][]string{}
	for checkType, claim := range claims {
		for _, standardID := range claim.Settles {
			judgedBy[standardID] = append(judgedBy[standardID], checkType)
		}
	}
	items, err := db.ListWorkItems(ctx, goalID)
	if err != nil {
		return decisions, err
	}
	project, err := db.ProjectByID(ctx, projectID)
	if err != nil {
		return decisions, err
	}
	ids := make([]string, 0, len(items))
	byID := map[string]int{}
	for i, item := range items {
		if item.Status != "DONE" {
			continue
		}
		if _, supplied := findings[item.ID]; !supplied {
			// Work a person wrote is not automation's to release.
			continue
		}
		ids = append(ids, item.ID)
		byID[item.ID] = i
	}
	sort.Strings(ids)
	// Releases already decided on. Collected rather than dropped, because
	// "nothing to approve" and "an approval is standing ready to spend" look
	// the same to an operator re-running the command and are not the same
	// thing: the second one is waiting for `goalforge merge`.
	var standing []string
	for _, workID := range ids {
		item := items[byID[workID]]
		standardID := findings[workID]
		gates := judgedBy[standardID]
		if reason := admits(policy, item, standardID, gates); reason != "" {
			decisions.Refused[workID] = reason
			continue
		}
		commit, err := db.LatestRunCommitForWork(ctx, projectID, workID)
		if errors.Is(err, store.ErrNotFound) {
			decisions.Refused[workID] = "검증된 커밋이 없습니다"
			continue
		}
		if err != nil {
			return decisions, err
		}
		if reason, err := gatesPassedOn(ctx, db, commit.RunID, gates); err != nil {
			return decisions, err
		} else if reason != "" {
			decisions.Refused[workID] = reason
			continue
		}
		scope := store.ApprovalScope{WorkItemID: workID, SourceBranch: commit.Branch,
			TargetRef: project.DefaultBranch, CommitSHA: commit.CommitSHA, FilesChanged: commit.FilesCommitted}
		// Nothing about finished work changes between two passes of the
		// unattended sweep, so without this the same release is approved again
		// every quarter of an hour: a stack of identical approvals, each one of
		// which `goalforge merge` will spend to release the same commit with no
		// review behind it, and a sweep that reports activity forever and so can
		// never be quiet again.
		//
		// This is not a second copy of the envelope — what the envelope permits
		// is admits()/AutoApprove's decision and is not repeated here. It asks
		// only whether this decision has already been taken, which is the same
		// question item.Status answers for execution approvals and which the
		// merge path has nothing to answer with, because merging does not move
		// the item off DONE.
		decided, err := db.ScopedApprovalExists(ctx, projectID, store.ApprovalMergeBranch, scope)
		if err != nil {
			return decisions, err
		}
		if decided {
			standing = append(standing, workID)
			continue
		}
		basis := fmt.Sprintf("자동 병합 승인 — 기준 %s · 게이트 %s · 커밋 %s",
			standardID, strings.Join(gates, ", "), shortSHA(commit.CommitSHA))
		approval, err := db.RequestScopedApproval(ctx, projectID, store.ApprovalMergeBranch, basis, scope)
		if err != nil {
			return decisions, err
		}
		if err = db.Approve(ctx, projectID, approval.ID); err != nil {
			return decisions, err
		}
		decisions.Approved = append(decisions.Approved, workID)
	}
	if len(standing) > 0 {
		decisions.Detail = fmt.Sprintf("이미 결정된 병합이라 다시 발급하지 않았습니다: %s", strings.Join(standing, ", "))
	}
	return decisions, nil
}

// gatesPassedOn reports why the gates that settle a criterion did not pass on
// the run that produced this commit.
//
// It reads the run's own results rather than the goal's latest, because the
// question is whether *this change* verified. A later run on a different work
// item passing tells nothing about this one.
func gatesPassedOn(ctx context.Context, db *store.Store, runID string, gates []string) (string, error) {
	results, err := db.VerificationsForRun(ctx, runID)
	if err != nil {
		return "", err
	}
	status := map[string]string{}
	for _, result := range results {
		status[result.CheckType] = result.Status
	}
	var missing, failed []string
	for _, checkType := range gates {
		switch state, ran := status[checkType]; {
		case !ran:
			missing = append(missing, checkType)
		case state != "PASSED":
			failed = append(failed, checkType+"="+state)
		}
	}
	sort.Strings(missing)
	sort.Strings(failed)
	if len(failed) > 0 {
		return "게이트가 통과하지 않았습니다: " + strings.Join(failed, ", "), nil
	}
	if len(missing) > 0 {
		// A gate that did not run on this change has not judged it. Releasing
		// on the strength of a gate nobody executed is releasing unverified.
		return "이 변경에서 돌지 않은 게이트가 있습니다: " + strings.Join(missing, ", "), nil
	}
	return "", nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

var _ = time.Now
