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
	// heartbeatInterval is how often a held lease is renewed. Zero means a
	// fraction of the lease duration, which is what production uses; tests
	// set it so a lease period fits inside a test.
	heartbeatInterval time.Duration
	repairPolicy      store.RepairPolicy
}
type ContinueResult struct {
	WorkItem     model.WorkItem
	Run          orchestrator.Result
	Verification verification.Report
	// Repair is the decision about a failed verification: what failed and
	// whether GoalForge may try again on its own.
	Repair store.RepairPlan
}
type IdeasResult struct {
	Run       orchestrator.Result
	Discovery planner.DiscoveryResult
}
type ResumeResult struct {
	Checkpoint   store.Checkpoint
	Run          orchestrator.Result
	Verification verification.Report
	Repair       store.RepairPlan
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
	return &Service{store: s, planner: p, orchestrator: o, verification: v, loopGuard: loopGuard, newRunID: newRunID,
		leaseDuration: 2 * time.Hour, repairPolicy: store.DefaultRepairPolicy()}, nil
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
	release, err := s.store.HoldLease(ctx, project.ID, runID, s.leaseDuration, s.heartbeatInterval)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, release()) }()
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
	release, err := s.store.HoldLease(ctx, project.ID, runID, s.leaseDuration, s.heartbeatInterval)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, release()) }()
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
	release, err := s.store.HoldLease(ctx, project.ID, runID, s.leaseDuration, s.heartbeatInterval)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, release()) }()
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
	resumedInWorktree := false
	if result.Checkpoint.WorkItemID != "" {
		worktree, worktreeErr := s.store.WorktreeForWorkItem(ctx, project.ID, result.Checkpoint.WorkItemID)
		if worktreeErr == nil {
			// The recorded path is checked before anything reads the tree.
			// Otherwise a worktree that was pruned, deleted, or created on
			// another machine surfaces as `fatal: cannot change to '...'`,
			// which reads like the repository is broken.
			recorded := gitops.Worktree{Path: worktree.Path, Branch: worktree.Branch, BaseCommit: worktree.BaseCommit}
			if intactErr := gitops.WorktreeIntact(ctx, recorded); intactErr != nil {
				return result, fmt.Errorf("%w — %s", intactErr, gitops.ExplainMissingWorktree(project.RepositoryPath, recorded))
			}
			project.RepositoryPath = worktree.Path
			resumedInWorktree = true
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
		result.Repair, err = s.recordVerificationLoop(ctx, project, result.Checkpoint.WorkItemID, result.Run.RunID, resumeChanges, result.Verification)
	}
	// Committed when the resume ran in a worktree, for the reason the fresh
	// path has: that branch is where the work is preserved, and left
	// uncommitted it cannot be merged, cannot be inherited by the next item,
	// and is lost when the worktree is cleaned. This is the path a run takes
	// after a quota wait or a block, so with the default settings the work of
	// every resumed run was being dropped.
	if err == nil && result.Verification.Passed && result.Checkpoint.WorkItemID != "" &&
		(resumedInWorktree || project.AutoCommitEnabled) {
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
		result = append(result, verification.Gate{Type: g.Type, Command: g.Command, Timeout: g.Timeout, Required: g.Required, SuccessValue: g.SuccessValue, ValuePattern: g.ValuePattern, Kind: g.Kind})
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
	// The lease carries a generation, so a cancel or a takeover part-way
	// through this run is detectable before anything is confirmed.
	lease, release, err := s.store.HoldGenerationLease(ctx, project.ID, runID, s.leaseDuration, s.heartbeatInterval)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, release()) }()
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
		// The last verified work for this goal, so what earlier items built is
		// there. Falling back to the default branch when nothing has been built
		// yet: a goal's first item has nothing to inherit.
		//
		// A failure to read it is not a reason to refuse the run — the branch
		// is a correct base, just an emptier one — but it is worth saying,
		// because a run that silently started from the wrong place is how the
		// next item fails to find a package that was written.
		base := ""
		if previous, baseErr := s.store.LatestGoalCommit(ctx, project.ID, goal.ID); baseErr == nil {
			base = previous.CommitSHA
		} else if !errors.Is(baseErr, store.ErrNotFound) {
			return result, fmt.Errorf("이전 작업의 커밋을 읽지 못했습니다: %w", baseErr)
		}
		worktree, worktreeErr := gitops.EnsureWorktree(ctx, project.RepositoryPath, project.ID, result.WorkItem.ID, base)
		if worktreeErr != nil {
			return result, fmt.Errorf("prepare worktree: %w", worktreeErr)
		}
		if worktreeErr = s.store.RecordWorktree(ctx, project.ID, result.WorkItem.ID, worktree); worktreeErr != nil {
			return result, worktreeErr
		}
		executionProject.RepositoryPath = worktree.Path
	}
	// The gates that will judge this run are captured before it starts. A
	// session that rewrites its own gate must not be able to certify itself.
	gateCommands := make([]policy.GateCommand, 0, len(gates))
	for _, g := range gates {
		gateCommands = append(gateCommands, policy.GateCommand{Type: g.Type, Command: g.Command})
	}
	surfaceBefore, err := policy.CaptureSurface(executionProject.RepositoryPath,
		policy.VerificationSurface(executionProject.RepositoryPath, gateCommands))
	if err != nil {
		return result, err
	}
	protectedBefore, err := policy.CaptureProtectedBaseline(ctx, executionProject.RepositoryPath)
	if err != nil {
		return result, fmt.Errorf("capture protected files: %w", err)
	}
	workspaceBefore, err := gitops.CaptureWorkspace(ctx, executionProject.RepositoryPath)
	if err != nil {
		return result, fmt.Errorf("capture workspace audit snapshot: %w", err)
	}
	estimatedTokens := result.WorkItem.EstimatedTokens
	if estimatedTokens == 0 {
		// No manual estimate: predict conservatively from recent run history
		// so the quota-warning large-work deferral still has a signal.
		if historical, estimateErr := s.store.EstimateWorkItemTokens(ctx, project.ID); estimateErr == nil {
			estimatedTokens = historical
		} else if !errors.Is(estimateErr, store.ErrNotFound) {
			return result, estimateErr
		}
	}
	rendered := prompt.Execution(goal, result.WorkItem, prompt.Budget{TokenLimit: budget.TokenLimit, TokensUsed: budget.TokensUsed, CostLimitUSD: budget.CostLimitUSD, CostUsedUSD: budget.CostUsedUSD})
	// The assembled context is what keeps a fresh session from re-deriving
	// settled decisions and repeating fixes that have already failed.
	contextPackage, err := s.store.BuildContextPackage(ctx, project, goal, result.WorkItem)
	if err != nil {
		return result, err
	}
	rendered = prompt.WithContext(rendered, contextSections(contextPackage))
	result.Run, err = s.orchestrator.Run(ctx, orchestrator.Request{RunID: runID, WorkItemID: result.WorkItem.ID, Prompt: rendered, PromptTemplate: "work_item_execution", TaskType: taskType, Project: executionProject, WorkspaceWrite: true, EstimatedTokens: estimatedTokens})
	auditErr := s.recordWorkspaceChanges(ctx, executionProject.RepositoryPath, runID, workspaceBefore)
	if err != nil || auditErr != nil {
		return result, errors.Join(err, auditErr)
	}
	// The work is done; whether it may be confirmed is a separate question. A
	// cancel or a takeover while the provider was running ends this tenancy,
	// and a late confirmation would overwrite whatever has been running since.
	if err = s.store.Fence(ctx, lease); err != nil {
		return result, err
	}
	if err = s.enforceProtectedFiles(ctx, executionProject, result.Run.RunID, protectedBefore); err != nil {
		return result, err
	}
	changes, err := s.store.ListRunFileChanges(ctx, result.Run.RunID)
	if err != nil {
		return result, err
	}
	if err = s.enforceVerificationIntegrity(ctx, project, executionProject, result.Run.RunID, surfaceBefore, changes); err != nil {
		return result, err
	}
	// Checked before the files are compared, because the two failures need
	// different remedies and the file list is the wrong thing to show for the
	// first one. A scope that is a sentence matches nothing, so every file
	// looks out of scope — and the reader goes to look at the files.
	//
	// Items filed before the scope form was checked are still on boards, so
	// this is what they say now.
	// An empty scope is not malformed, it is restrictive: OutOfScopeChanges
	// reads it as "may change nothing", which is a deliberate decision and
	// gets the file-list message. Only a non-empty scope that cannot match
	// anything is the malformed case.
	if result.WorkItem.ChangeScope != "" && !policy.UsableScope(result.WorkItem.ChangeScope) {
		details := fmt.Sprintf("이 작업의 변경 범위가 경로 목록이 아닙니다 (%q) — 바꿀 파일 경로나 glob 을 쉼표로 구분해 적어야 합니다. `goalforge work scope --item %s --set <경로>` 로 고치세요",
			truncateForMessage(result.WorkItem.ChangeScope), result.WorkItem.ID)
		if recordErr := s.store.RecordPolicyViolation(ctx, project.ID, result.Run.RunID, "GOAL_DRIFT", details); recordErr != nil {
			return result, errors.Join(errors.New(details), recordErr)
		}
		return result, errors.New(details)
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
		verificationGates = append(verificationGates, verification.Gate{Type: g.Type, Command: g.Command, Timeout: g.Timeout, Required: g.Required, SuccessValue: g.SuccessValue, ValuePattern: g.ValuePattern, Kind: g.Kind})
	}
	result.Verification, err = s.verification.Verify(ctx, result.Run.RunID, executionProject, verificationGates)
	if err == nil {
		result.Repair, err = s.recordVerificationLoop(ctx, project, result.WorkItem.ID, result.Run.RunID, changes, result.Verification)
	}
	if err == nil {
		err = s.settleAutomaticAttempt(ctx, result)
	}
	// Verified work in an isolated worktree is committed on its own branch
	// whatever the auto-commit setting says.
	//
	// That branch is where the work is preserved. Left uncommitted it is dirty
	// files in a worktree: nothing can merge it, the next item cannot inherit
	// it, and cleaning the worktree loses it — so with the default settings a
	// goal made of more than one item could never be finished.
	//
	// The flag was not what kept this safe. CommitVerified refuses the
	// protected branch itself, so a run that was not isolated — one sitting on
	// the default branch — is still refused, and that is the case the flag is
	// about. Committing on an isolated branch is not publishing; nothing
	// reaches the default branch without an approval.
	if err == nil && result.Verification.Passed && (isolateWorkItem || project.AutoCommitEnabled) {
		err = s.commitVerifiedRun(ctx, project, executionProject.RepositoryPath, goal.ID, result.WorkItem.ID, result.WorkItem.Title, result.Run.RunID)
	}
	return result, err
}

// settleAutomaticAttempt closes out the automatic approval this run came from.
//
// The autonomy loop refuses to approve again an item whose previous automatic
// attempt was settled and failed; without this, no attempt was ever settled
// and that guard never fired. The loop would re-approve a failing item every
// sweep and spend the day's allowance reaching the same place.
//
// Settled only when the attempt has actually ended. A failure the repair
// policy will retry on its own is still outstanding, and calling it finished
// would refuse the retry the policy just granted.
func (s *Service) settleAutomaticAttempt(ctx context.Context, result ContinueResult) error {
	if result.WorkItem.ID == "" {
		return nil
	}
	if !result.Verification.Passed && result.Repair.Automatic() {
		return nil
	}
	detail := automaticAttemptDetail(result)
	err := s.store.SettleAutoApproval(ctx, result.WorkItem.ID, result.Verification.Passed, detail)
	if errors.Is(err, store.ErrNotFound) {
		// A person approved this one. There is no automatic attempt to close,
		// and refusing the run over it would make manual approval fail.
		return nil
	}
	return err
}

// automaticAttemptDetail says in one line why the attempt ended, naming the
// gates that failed.
//
// The gate names rather than "verification failed": the next reader decides
// whether to try again from this sentence, and a sentence that does not say
// what stopped it sends them back to the logs to find out.
func automaticAttemptDetail(result ContinueResult) string {
	if result.Verification.Passed {
		return fmt.Sprintf("게이트 %d건 통과", len(result.Verification.Results))
	}
	var failed []string
	for _, gate := range result.Verification.Results {
		if gate.Status != "PASSED" {
			failed = append(failed, gate.Type+" ("+gate.Status+")")
		}
	}
	if len(failed) == 0 {
		// Nothing individually failed and the report did not pass, so the
		// reason is in the plan rather than in a gate.
		if result.Repair.Reason != "" {
			return result.Repair.Reason
		}
		return "검증이 통과하지 못했습니다"
	}
	return "실패한 게이트: " + strings.Join(failed, ", ")
}

// truncateForMessage keeps an error readable when the value quoted is a
// paragraph.
func truncateForMessage(text string) string {
	const limit = 60
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// contextSections converts an assembled context package into prompt sections.
func contextSections(pkg store.ContextPackage) []prompt.ContextSection {
	section := func(heading string, items []store.ContextItem) prompt.ContextSection {
		lines := make([]prompt.ContextLine, 0, len(items))
		for _, item := range items {
			line := prompt.ContextLine{Title: item.Title, Body: item.Body, Source: item.Source,
				Standing: item.Standing, Caveat: item.Caveat}
			if !item.AsOf.IsZero() {
				line.AsOf = item.AsOf.Format("2006-01-02")
			}
			lines = append(lines, line)
		}
		return prompt.ContextSection{Heading: heading, Items: lines}
	}
	return []prompt.ContextSection{
		section("이미 내려진 설계 결정", pkg.Decisions),
		section("변경 제약", pkg.Constraints),
		section("이 작업의 이전 실패", pkg.PastFailures),
		section("검증 방법", pkg.Verification),
	}
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
// recordVerificationLoop records loop-guard signals for a failed verification
// and decides whether the failure may be repaired automatically. The decision
// is made here because this is the one place that sees every failed run.
func (s *Service) recordVerificationLoop(ctx context.Context, project model.Project, workItemID, runID string, changes []gitops.FileChange, report verification.Report) (store.RepairPlan, error) {
	var plan store.RepairPlan
	if report.Passed {
		return plan, nil
	}
	plan, planErr := s.store.PlanRepair(ctx, runID, s.repairPolicy)
	if planErr != nil {
		return plan, planErr
	}
	var failed []string
	for _, result := range report.Results {
		if result.Required && result.Status != "PASSED" {
			failed = append(failed, result.Type+"\x00"+result.Status+"\x00"+result.Output)
		}
	}
	if len(failed) == 0 {
		return plan, nil
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(failed, "\x00"))))
	if _, _, err := s.loopGuard.Record(ctx, project.ID, workItemID, "same_error", fingerprint, runID); err != nil {
		return plan, err
	}
	if workItemID != "" {
		if _, _, err := s.loopGuard.Record(ctx, project.ID, workItemID, "same_work", workItemID, runID); err != nil {
			return plan, err
		}
	}
	if len(changes) == 0 {
		action, _, err := s.loopGuard.Record(ctx, project.ID, workItemID, "no_change", "no-change:"+workItemID, runID)
		if err != nil {
			return plan, err
		}
		if action == planner.LoopRotateSession {
			return plan, s.rotateSessionForLoop(ctx, project, "no_change_loop: repeated completion claims without file changes")
		}
		return plan, nil
	}
	var parts []string
	for _, change := range changes {
		parts = append(parts, change.Path+"\x00"+change.ChangeType+"\x00"+change.AfterHash)
	}
	changeFingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, "\x00"))))
	_, _, err := s.loopGuard.Record(ctx, project.ID, workItemID, "same_change", changeFingerprint, runID)
	return plan, err
}

// SetRepairPolicy overrides the automatic-repair limits.
func (s *Service) SetRepairPolicy(repairPolicy store.RepairPolicy) { s.repairPolicy = repairPolicy }

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

// enforceVerificationIntegrity keeps a run from certifying itself. Two things
// make a verdict worthless: a gate the session rewrote, and the removal of the
// tests that were supposed to fail. Neither is treated as a reason to discard
// the run — the work may be fine — but the judgement has to come from the
// gates that were agreed, so those are restored before verification and the
// change is recorded for review rather than silently dropped.
func (s *Service) enforceVerificationIntegrity(ctx context.Context, project, executionProject model.Project, runID string, before policy.SurfaceBaseline, changes []gitops.FileChange) error {
	changedGates, err := before.Changed(executionProject.RepositoryPath)
	if err != nil {
		return fmt.Errorf("inspect verification files: %w", err)
	}
	if len(changedGates) > 0 {
		if err = before.Restore(executionProject.RepositoryPath); err != nil {
			details := "restore verification files after in-run modification: " + err.Error()
			return errors.Join(errors.New(details), s.store.RecordPolicyViolation(ctx, project.ID, runID, "VERIFICATION_RESTORE_FAILED", details))
		}
		if err = s.store.RecordRelaxation(ctx, store.VerificationRelaxation{ProjectID: project.ID, RunID: runID,
			Kind: "gate_modified_in_run", Detail: "실행 중 검증 게이트 파일이 수정되어 원래 내용으로 되돌렸습니다: " + strings.Join(changedGates, ", "),
			Before: "합의된 게이트", After: "실행이 수정한 게이트 (되돌림)"}); err != nil {
			return err
		}
	}
	removed := policy.RemovedTests(changes)
	if len(removed) == 0 {
		return nil
	}
	if err = s.store.RecordRelaxation(ctx, store.VerificationRelaxation{ProjectID: project.ID, RunID: runID,
		Kind: "tests_deleted", Detail: "삭제된 테스트 파일: " + strings.Join(removed, ", "),
		Before: fmt.Sprintf("%d개 테스트 파일", len(removed)), After: "삭제됨"}); err != nil {
		return err
	}
	// Removing tests that already existed can be right, but it is not
	// something a run may decide for itself: without an approval the run is
	// stopped rather than allowed to pass gates it just made easier.
	approved, err := s.store.ConsumeApproval(ctx, project.ID, store.ApprovalRemoveTests, runID)
	if err != nil {
		return err
	}
	if approved {
		return nil
	}
	details := "tests removed without approval: " + strings.Join(removed, ", ")
	return errors.Join(errors.New(details), s.store.RecordPolicyViolation(ctx, project.ID, runID, "TESTS_REMOVED", details))
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
