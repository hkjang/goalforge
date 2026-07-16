package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/orchestrator"
	"github.com/goalforge/goalforge/internal/planner"
	"github.com/goalforge/goalforge/internal/policy"
	"github.com/goalforge/goalforge/internal/prompt"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/verification"
)

type Service struct {
	store         *store.Store
	planner       *planner.Service
	orchestrator  *orchestrator.Orchestrator
	verification  *verification.Engine
	loopGuard     *planner.LoopGuard
	newRunID      func() string
	leaseDuration time.Duration
}
type ContinueResult struct {
	WorkItem     model.WorkItem
	Run          orchestrator.Result
	Verification verification.Report
}
type IdeasResult struct {
	Run       orchestrator.Result
	Discovery planner.DiscoveryResult
}
type ResumeResult struct {
	Checkpoint   store.Checkpoint
	Run          orchestrator.Result
	Verification verification.Report
}

func New(s *store.Store, p *planner.Service, o *orchestrator.Orchestrator, v *verification.Engine, newRunID func() string) (*Service, error) {
	if s == nil || p == nil || o == nil || v == nil {
		return nil, errors.New("store, planner, orchestrator, and verification are required")
	}
	if newRunID == nil {
		newRunID = func() string { return store.NewID("RUN") }
	}
	loopGuard, err := planner.NewLoopGuard(s, planner.DefaultLoopPolicy())
	if err != nil {
		return nil, err
	}
	return &Service{store: s, planner: p, orchestrator: o, verification: v, loopGuard: loopGuard, newRunID: newRunID, leaseDuration: 2 * time.Hour}, nil
}

// Ideas discovers new goal-contributing work candidates (DISCOVER_IDEAS).
func (s *Service) Ideas(ctx context.Context, project model.Project) (IdeasResult, error) {
	return s.discover(ctx, project, prompt.Ideas, "idea_discovery", model.TaskDiscoverIdeas)
}

// Audit inspects the repository for quality, security, performance, UI/UX,
// and operability problems and files improvements (AUDIT_AND_IMPROVE).
func (s *Service) Audit(ctx context.Context, project model.Project) (IdeasResult, error) {
	return s.discover(ctx, project, prompt.Audit, "audit_and_improve", model.TaskAuditAndImprove)
}

func (s *Service) discover(ctx context.Context, project model.Project, render func(model.Goal, []model.WorkItem) string, template, taskType string) (result IdeasResult, err error) {
	runID := s.newRunID()
	if err = s.store.AcquireLease(ctx, project.ID, runID, time.Now().UTC(), s.leaseDuration); err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, s.store.ReleaseLease(context.WithoutCancel(ctx), project.ID, runID)) }()
	goal, err := s.store.CurrentGoal(ctx, project.ID)
	if err != nil {
		return result, err
	}
	if err = s.planner.CanDiscover(ctx, goal.ID); err != nil {
		return result, err
	}
	existing, err := s.store.ListWorkItems(ctx, goal.ID)
	if err != nil {
		return result, err
	}
	result.Run, err = s.orchestrator.Run(ctx, orchestrator.Request{
		RunID: runID, Prompt: render(goal, existing), OutputSchema: prompt.IdeasSchema(),
		PromptTemplate: template, TaskType: taskType, Project: project, ReadOnlyTask: true, Isolated: true,
	})
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(result.Run.FinalMessage) == "" {
		return result, errors.New("provider returned no structured idea result")
	}
	var response struct {
		Ideas []planner.Candidate `json:"ideas"`
	}
	if err = json.Unmarshal([]byte(result.Run.FinalMessage), &response); err != nil {
		return result, fmt.Errorf("decode structured idea result: %w", err)
	}
	result.Discovery, err = s.planner.DiscoverAndStore(ctx, goal.ID, response.Ideas)
	return result, err
}

// StaleItem is a backlog entry replanning flagged as no longer serving the
// goal. Applied entries were moved to BLOCKED for user review; nothing is
// discarded automatically.
type StaleItem struct {
	ID, Reason, Note string
	Applied          bool
}
type ReplanResult struct {
	Run       orchestrator.Result
	Discovery planner.DiscoveryResult
	Stale     []StaleItem
}

// Replan compares the implementation against the goal (REPLAN_GOAL): gap work
// items flow through the discovery pipeline and stale backlog entries are
// flagged BLOCKED for review.
func (s *Service) Replan(ctx context.Context, project model.Project) (result ReplanResult, err error) {
	runID := s.newRunID()
	if err = s.store.AcquireLease(ctx, project.ID, runID, time.Now().UTC(), s.leaseDuration); err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, s.store.ReleaseLease(context.WithoutCancel(ctx), project.ID, runID)) }()
	goal, err := s.store.CurrentGoal(ctx, project.ID)
	if err != nil {
		return result, err
	}
	existing, err := s.store.ListWorkItems(ctx, goal.ID)
	if err != nil {
		return result, err
	}
	result.Run, err = s.orchestrator.Run(ctx, orchestrator.Request{
		RunID: runID, Prompt: prompt.Replan(goal, existing), OutputSchema: prompt.ReplanSchema(),
		PromptTemplate: "goal_replan", TaskType: model.TaskReplanGoal, Project: project, ReadOnlyTask: true, Isolated: true,
	})
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(result.Run.FinalMessage) == "" {
		return result, errors.New("provider returned no structured replan result")
	}
	var response struct {
		Gaps  []planner.Candidate `json:"gaps"`
		Stale []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"stale_items"`
	}
	if err = json.Unmarshal([]byte(result.Run.FinalMessage), &response); err != nil {
		return result, fmt.Errorf("decode structured replan result: %w", err)
	}
	// Flag stale items first: sending them to review frees backlog capacity
	// before the gap candidates pass the unimplemented-work limit.
	byID := make(map[string]model.WorkItem, len(existing))
	for _, item := range existing {
		byID[item.ID] = item
	}
	for _, stale := range response.Stale {
		entry := StaleItem{ID: stale.ID, Reason: stale.Reason}
		item, known := byID[stale.ID]
		switch {
		case !known:
			entry.Note = "unknown work item"
		case item.Status != "BACKLOG" && item.Status != "APPROVED":
			entry.Note = "only BACKLOG or APPROVED items can be flagged"
		default:
			if statusErr := s.store.SetWorkItemStatus(ctx, goal.ID, stale.ID, "BLOCKED"); statusErr != nil {
				entry.Note = statusErr.Error()
			} else {
				entry.Applied = true
			}
		}
		result.Stale = append(result.Stale, entry)
	}
	result.Discovery, err = s.planner.DiscoverAndStore(ctx, goal.ID, response.Gaps)
	return result, err
}

func (s *Service) ResumePaused(ctx context.Context, project model.Project) (result ResumeResult, err error) {
	runID := s.newRunID()
	if err = s.store.AcquireLease(ctx, project.ID, runID, time.Now().UTC(), s.leaseDuration); err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, s.store.ReleaseLease(context.WithoutCancel(ctx), project.ID, runID)) }()
	project, err = s.store.ProjectByID(ctx, project.ID)
	if err != nil {
		return result, err
	}
	if project.State != "BLOCKED" {
		return result, fmt.Errorf("project state is %s, expected BLOCKED", project.State)
	}
	result.Checkpoint, err = s.store.LatestCheckpoint(ctx, project.ID)
	if err != nil {
		return result, err
	}
	if result.Checkpoint.WorkItemID != "" {
		worktree, worktreeErr := s.store.WorktreeForWorkItem(ctx, project.ID, result.Checkpoint.WorkItemID)
		if worktreeErr == nil {
			project.RepositoryPath = worktree.Path
		} else if project.WorktreeEnabled || !errors.Is(worktreeErr, store.ErrNotFound) {
			return result, fmt.Errorf("load checkpoint worktree: %w", worktreeErr)
		}
	}
	current, err := (gitops.GitInspector{}).Snapshot(ctx, project.RepositoryPath)
	if err != nil {
		return result, err
	}
	saved := gitops.Snapshot{CommitSHA: result.Checkpoint.CommitSHA, Branch: result.Checkpoint.Branch, DirtyFiles: result.Checkpoint.DirtyFiles, DirtyFingerprint: result.Checkpoint.DirtyFingerprint}
	if err = gitops.EqualSnapshot(saved, current); err != nil {
		return result, fmt.Errorf("repository changed after checkpoint: %w", err)
	}
	if result.Checkpoint.SessionID != "" {
		session, sessionErr := s.store.ActiveSession(ctx, project.ID, project.Provider)
		if sessionErr != nil || session.SessionID != result.Checkpoint.SessionID {
			return result, errors.New("saved provider session no longer matches checkpoint")
		}
	}
	gates, err := s.store.ListGates(ctx, project.ID)
	if err != nil {
		return result, err
	}
	verificationGates, err := requiredGates(gates)
	if err != nil {
		return result, err
	}
	protectedBefore, err := policy.CaptureProtectedBaseline(ctx, project.RepositoryPath)
	if err != nil {
		return result, fmt.Errorf("capture protected files: %w", err)
	}
	if err = s.store.TransitionProjectState(ctx, project.ID, "BLOCKED", "RESUMING"); err != nil {
		return result, err
	}
	project.State = "RESUMING"
	workspaceBefore, err := gitops.CaptureWorkspace(ctx, project.RepositoryPath)
	if err != nil {
		return result, err
	}
	result.Run, err = s.orchestrator.Run(ctx, orchestrator.Request{RunID: runID, WorkItemID: result.Checkpoint.WorkItemID, Prompt: orchestrator.BuildResumePrompt(result.Checkpoint), PromptTemplate: "checkpoint_resume", TaskType: model.TaskContinueGoal, Project: project, WorkspaceWrite: true})
	auditErr := s.recordWorkspaceChanges(ctx, project.RepositoryPath, runID, workspaceBefore)
	if err != nil || auditErr != nil {
		return result, errors.Join(err, auditErr)
	}
	if err = s.enforceProtectedFiles(ctx, project, result.Run.RunID, protectedBefore); err != nil {
		return result, err
	}
	result.Verification, err = s.verification.Verify(ctx, result.Run.RunID, project, verificationGates)
	if err == nil {
		resumeChanges, changesErr := s.store.ListRunFileChanges(ctx, result.Run.RunID)
		if changesErr != nil {
			return result, changesErr
		}
		err = s.recordVerificationLoop(ctx, project, result.Checkpoint.WorkItemID, result.Run.RunID, resumeChanges, result.Verification)
	}
	if err == nil && result.Verification.Passed && project.AutoCommitEnabled && result.Checkpoint.WorkItemID != "" {
		goal, goalErr := s.store.CurrentGoal(ctx, project.ID)
		if goalErr != nil {
			return result, goalErr
		}
		err = s.commitVerifiedRun(ctx, project, project.RepositoryPath, goal.ID, result.Checkpoint.WorkItemID, "", result.Run.RunID)
	}
	return result, err
}

func requiredGates(gates []store.GateConfig) ([]verification.Gate, error) {
	if len(gates) == 0 {
		return nil, errors.New("no verification gates configured")
	}
	required := false
	result := make([]verification.Gate, 0, len(gates))
	for _, g := range gates {
		required = required || g.Required
		result = append(result, verification.Gate{Type: g.Type, Command: g.Command, Timeout: g.Timeout, Required: g.Required, SuccessValue: g.SuccessValue})
	}
	if !required {
		return nil, errors.New("at least one required verification gate must be configured")
	}
	return result, nil
}

// Continue performs the highest-priority executable work item (CONTINUE_GOAL).
func (s *Service) Continue(ctx context.Context, project model.Project) (ContinueResult, error) {
	return s.executeNext(ctx, project, model.TaskContinueGoal)
}

// Develop implements the approved or highest-scored idea (IMPLEMENT_SELECTED).
func (s *Service) Develop(ctx context.Context, project model.Project) (ContinueResult, error) {
	return s.executeNext(ctx, project, model.TaskImplementSelected)
}

func (s *Service) executeNext(ctx context.Context, project model.Project, taskType string) (result ContinueResult, err error) {
	runID := s.newRunID()
	if err = s.store.AcquireLease(ctx, project.ID, runID, time.Now().UTC(), s.leaseDuration); err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, s.store.ReleaseLease(context.WithoutCancel(ctx), project.ID, runID)) }()
	project, err = s.store.ProjectByID(ctx, project.ID)
	if err != nil {
		return result, err
	}
	if project.State == "REPAIR_REQUIRED" {
		if err = s.store.TransitionProjectState(ctx, project.ID, "REPAIR_REQUIRED", "READY"); err != nil {
			return result, err
		}
		project.State = "READY"
	}
	goal, err := s.store.CurrentGoal(ctx, project.ID)
	if err != nil {
		return result, err
	}
	gates, err := s.store.ListGates(ctx, project.ID)
	if err != nil {
		return result, err
	}
	if len(gates) == 0 {
		return result, errors.New("no verification gates configured")
	}
	required := false
	for _, g := range gates {
		required = required || g.Required
	}
	if !required {
		return result, errors.New("at least one required verification gate must be configured")
	}
	budget := store.ProjectBudget{}
	if b, budgetErr := s.store.ProjectBudgetUsage(ctx, project.ID); budgetErr == nil {
		budget = b
	} else if !errors.Is(budgetErr, store.ErrNotFound) {
		return result, budgetErr
	}
	if budget.TokenLimit > 0 && budget.TokensUsed >= budget.TokenLimit {
		return result, errors.New("project token budget exhausted")
	}
	if budget.CostLimitUSD > 0 && budget.CostUsedUSD >= budget.CostLimitUSD {
		return result, errors.New("project cost budget exhausted")
	}
	result.WorkItem, err = s.planner.SelectNext(ctx, goal.ID)
	if err != nil {
		return result, err
	}
	executionProject := project
	isolateWorkItem := project.WorktreeEnabled
	if !isolateWorkItem {
		if _, snapshotErr := (gitops.GitInspector{}).Snapshot(ctx, project.RepositoryPath); snapshotErr == nil {
			isolateWorkItem = true
		}
	}
	if isolateWorkItem {
		worktree, worktreeErr := gitops.EnsureWorktree(ctx, project.RepositoryPath, project.ID, result.WorkItem.ID)
		if worktreeErr != nil {
			return result, fmt.Errorf("prepare worktree: %w", worktreeErr)
		}
		if worktreeErr = s.store.RecordWorktree(ctx, project.ID, result.WorkItem.ID, worktree); worktreeErr != nil {
			return result, worktreeErr
		}
		executionProject.RepositoryPath = worktree.Path
	}
	protectedBefore, err := policy.CaptureProtectedBaseline(ctx, executionProject.RepositoryPath)
	if err != nil {
		return result, fmt.Errorf("capture protected files: %w", err)
	}
	workspaceBefore, err := gitops.CaptureWorkspace(ctx, executionProject.RepositoryPath)
	if err != nil {
		return result, fmt.Errorf("capture workspace audit snapshot: %w", err)
	}
	rendered := prompt.Execution(goal, result.WorkItem, prompt.Budget{TokenLimit: budget.TokenLimit, TokensUsed: budget.TokensUsed, CostLimitUSD: budget.CostLimitUSD, CostUsedUSD: budget.CostUsedUSD})
	result.Run, err = s.orchestrator.Run(ctx, orchestrator.Request{RunID: runID, WorkItemID: result.WorkItem.ID, Prompt: rendered, PromptTemplate: "work_item_execution", TaskType: taskType, Project: executionProject, WorkspaceWrite: true, EstimatedTokens: result.WorkItem.EstimatedTokens})
	auditErr := s.recordWorkspaceChanges(ctx, executionProject.RepositoryPath, runID, workspaceBefore)
	if err != nil || auditErr != nil {
		return result, errors.Join(err, auditErr)
	}
	if err = s.enforceProtectedFiles(ctx, executionProject, result.Run.RunID, protectedBefore); err != nil {
		return result, err
	}
	changes, err := s.store.ListRunFileChanges(ctx, result.Run.RunID)
	if err != nil {
		return result, err
	}
	if drift := policy.OutOfScopeChanges(result.WorkItem.ChangeScope, changes); len(drift) > 0 {
		details := "work item changed files outside declared scope: " + strings.Join(drift, ", ")
		if recordErr := s.store.RecordPolicyViolation(ctx, project.ID, result.Run.RunID, "GOAL_DRIFT", details); recordErr != nil {
			return result, errors.Join(errors.New(details), recordErr)
		}
		return result, errors.New(details)
	}
	verificationGates := make([]verification.Gate, 0, len(gates))
	for _, g := range gates {
		verificationGates = append(verificationGates, verification.Gate{Type: g.Type, Command: g.Command, Timeout: g.Timeout, Required: g.Required, SuccessValue: g.SuccessValue})
	}
	result.Verification, err = s.verification.Verify(ctx, result.Run.RunID, executionProject, verificationGates)
	if err == nil {
		err = s.recordVerificationLoop(ctx, project, result.WorkItem.ID, result.Run.RunID, changes, result.Verification)
	}
	if err == nil && result.Verification.Passed && project.AutoCommitEnabled {
		err = s.commitVerifiedRun(ctx, project, executionProject.RepositoryPath, goal.ID, result.WorkItem.ID, result.WorkItem.Title, result.Run.RunID)
	}
	return result, err
}

// commitVerifiedRun commits a verified run's changes with Goal/Work/Run
// trailers (GIT-009). It only runs after verification passes (GIT-008) and
// records the resulting commit for audit.
func (s *Service) commitVerifiedRun(ctx context.Context, project model.Project, repository, goalID, workItemID, title, runID string) error {
	commit, err := gitops.CommitVerified(ctx, repository, project.DefaultBranch, goalID, workItemID, runID, title)
	if err != nil {
		return fmt.Errorf("commit verified run: %w", err)
	}
	if commit.CommitSHA == "" {
		return nil
	}
	return s.store.RecordRunCommit(ctx, store.RunCommit{RunID: runID, ProjectID: project.ID, GoalID: goalID, WorkItemID: workItemID, CommitSHA: commit.CommitSHA, Branch: commit.Branch, FilesCommitted: commit.FilesCommitted})
}

func (s *Service) recordWorkspaceChanges(ctx context.Context, repository, runID string, before gitops.WorkspaceSnapshot) error {
	after, err := gitops.CaptureWorkspace(ctx, repository)
	if err != nil {
		return fmt.Errorf("capture post-run workspace audit snapshot: %w", err)
	}
	if err = s.store.RecordRunFileChanges(ctx, runID, gitops.ChangedFiles(before, after)); err != nil {
		return fmt.Errorf("record run file changes: %w", err)
	}
	return nil
}

// recordVerificationLoop feeds the loop guard after a failed verification:
// same_error fingerprints identical gate output (LOOP-004), same_work counts
// repeated failing runs on one item (LOOP-002), no_change catches completion
// claims without any file change (LOOP-005, answered with a session rotation
// before any block), and same_change catches runs that keep producing an
// identical change set (LOOP-003).
func (s *Service) recordVerificationLoop(ctx context.Context, project model.Project, workItemID, runID string, changes []gitops.FileChange, report verification.Report) error {
	if report.Passed {
		return nil
	}
	var failed []string
	for _, result := range report.Results {
		if result.Required && result.Status != "PASSED" {
			failed = append(failed, result.Type+"\x00"+result.Status+"\x00"+result.Output)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(failed, "\x00"))))
	if _, _, err := s.loopGuard.Record(ctx, project.ID, workItemID, "same_error", fingerprint, runID); err != nil {
		return err
	}
	if workItemID != "" {
		if _, _, err := s.loopGuard.Record(ctx, project.ID, workItemID, "same_work", workItemID, runID); err != nil {
			return err
		}
	}
	if len(changes) == 0 {
		action, _, err := s.loopGuard.Record(ctx, project.ID, workItemID, "no_change", "no-change:"+workItemID, runID)
		if err != nil {
			return err
		}
		if action == planner.LoopRotateSession {
			return s.rotateSessionForLoop(ctx, project, "no_change_loop: repeated completion claims without file changes")
		}
		return nil
	}
	var parts []string
	for _, change := range changes {
		parts = append(parts, change.Path+"\x00"+change.ChangeType+"\x00"+change.AfterHash)
	}
	changeFingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, "\x00"))))
	_, _, err := s.loopGuard.Record(ctx, project.ID, workItemID, "same_change", changeFingerprint, runID)
	return err
}

// rotateSessionForLoop retires the active provider session so the next run
// starts fresh instead of continuing a conversation that stopped producing
// real changes.
func (s *Service) rotateSessionForLoop(ctx context.Context, project model.Project, reason string) error {
	session, err := s.store.ActiveSession(ctx, project.ID, project.Provider)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	err = s.store.InvalidateSession(ctx, project.ID, project.Provider, session.SessionID, reason, 7*24*time.Hour)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Service) enforceProtectedFiles(ctx context.Context, project model.Project, runID string, before policy.ProtectedBaseline) error {
	after, err := policy.CaptureProtected(ctx, project.RepositoryPath)
	if err != nil {
		recordErr := s.store.RecordPolicyViolation(ctx, project.ID, runID, "PROTECTED_FILE_SCAN_FAILED", err.Error())
		return errors.Join(fmt.Errorf("verify protected files: %w", err), recordErr)
	}
	changed := policy.ChangedProtected(before.Snapshot(), after)
	if len(changed) == 0 {
		return nil
	}
	approved, err := s.store.ConsumeApproval(ctx, project.ID, store.ApprovalProtectedFiles, runID)
	if err != nil {
		return err
	}
	if approved {
		return nil
	}
	if err = before.Restore(ctx, project.RepositoryPath); err != nil {
		restoreDetails := "restore protected files after unapproved change: " + err.Error()
		recordErr := s.store.RecordPolicyViolation(ctx, project.ID, runID, "PROTECTED_FILE_RESTORE_FAILED", restoreDetails)
		return errors.Join(errors.New(restoreDetails), recordErr)
	}
	details := "protected files changed without approval: " + strings.Join(changed, ", ")
	if err = s.store.RecordPolicyViolation(ctx, project.ID, runID, "PROTECTED_FILE_CHANGED", details); err != nil {
		return err
	}
	return errors.New(details)
}
