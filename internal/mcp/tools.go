package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

var errUnknownTool = errors.New("unknown tool")

// triageStatuses mirrors the dashboard write API: MCP clients may triage the
// backlog but never flip execution states, which stay orchestrator-owned.
var triageStatuses = map[string]bool{"APPROVED": true, "BLOCKED": true, "DISCARDED": true, "BACKLOG": true}

type toolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func schema(required []string, properties map[string]any) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	result := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func numberProperty(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

var projectProperty = stringProperty("Project ID or name. May be omitted when exactly one project is registered.")

func toolDescriptors() []toolDescriptor {
	return []toolDescriptor{
		{"list_projects", "List every registered project with its state, goal, and progress.", schema(nil, nil)},
		{"project_status", "Full status of one project: state, goal, progress, criteria evidence, metrics.", schema(nil, map[string]any{"project": projectProperty})},
		{"goal_show", "Show the active (or latest) goal with completion criteria.", schema(nil, map[string]any{"project": projectProperty})},
		{"goal_set", "Set a new goal version. Requires a reason after the first version.", schema([]string{"title", "objective"}, map[string]any{"project": projectProperty, "title": stringProperty("Goal title."), "objective": stringProperty("What done means."), "reason": stringProperty("Why the goal changed (required after v1)."), "criteria": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Completion criteria as type=value pairs, e.g. build_passed=true."}})},
		{"work_list", "List the active goal's work items with status, priority, and idea scores.", schema(nil, map[string]any{"project": projectProperty})},
		{"work_add", "Add a work item to the active goal's backlog.", schema([]string{"title"}, map[string]any{"project": projectProperty, "title": stringProperty("Work item title."), "priority": numberProperty("Selection priority (higher first)."), "scope": stringProperty("Allowed change scope, e.g. internal/session/**."), "type": stringProperty("Work type (default IMPLEMENT)."), "estimated_tokens": numberProperty("Manual token estimate for one run.")})},
		{"work_set_status", "Triage a backlog item: APPROVED, BLOCKED, DISCARDED, or BACKLOG. Execution states are refused.", schema([]string{"work_item_id", "status"}, map[string]any{"project": projectProperty, "work_item_id": stringProperty("Work item ID."), "status": stringProperty("APPROVED | BLOCKED | DISCARDED | BACKLOG")})},
		{"approvals_list", "List pending approvals across all projects.", schema(nil, nil)},
		{"approval_request", "Request an approval for a gated action (protected-files, merge-branch, publish-branch).", schema([]string{"action", "reason"}, map[string]any{"project": projectProperty, "action": stringProperty("protected-files | merge-branch | publish-branch"), "reason": stringProperty("Why the action is needed.")})},
		{"approval_decide", "Approve or reject one pending approval.", schema([]string{"approval_id", "decision"}, map[string]any{"project": projectProperty, "approval_id": stringProperty("Approval ID (APR-...)."), "decision": stringProperty("approve | reject")})},
		{"usage_report", "Token and cost usage, budgets, and provider quota windows for a project.", schema(nil, map[string]any{"project": projectProperty})},
		{"runs_recent", "Recent runs with task type, work item, tokens, and state.", schema(nil, map[string]any{"project": projectProperty, "limit": numberProperty("Maximum runs to return (default 10).")})},
		{"run_detail", "Replay one run from audit records: prompt template and hash, usage, gates, file changes, commit.", schema([]string{"run_id"}, map[string]any{"project": projectProperty, "run_id": stringProperty("Run ID (RUN-...).")})},
		{"continue_enqueue", "Schedule a persistent CONTINUE job so a running `goalforge worker` executes work items one at a time toward the goal.", schema(nil, map[string]any{"project": projectProperty})},
		{"checkpoint_create", "Create a manual recovery checkpoint (also writes the CONTINUITY.md companion).", schema([]string{"next_action"}, map[string]any{"project": projectProperty, "next_action": stringProperty("The first concrete step on resume."), "completed": stringProperty("What was completed."), "remaining": stringProperty("What remains."), "risks": stringProperty("Open risks.")})},
	}
}

type toolArgs struct {
	Project         string   `json:"project"`
	Title           string   `json:"title"`
	Objective       string   `json:"objective"`
	Reason          string   `json:"reason"`
	Criteria        []string `json:"criteria"`
	Priority        float64  `json:"priority"`
	Scope           string   `json:"scope"`
	Type            string   `json:"type"`
	EstimatedTokens int64    `json:"estimated_tokens"`
	WorkItemID      string   `json:"work_item_id"`
	Status          string   `json:"status"`
	Action          string   `json:"action"`
	ApprovalID      string   `json:"approval_id"`
	Decision        string   `json:"decision"`
	Limit           int      `json:"limit"`
	RunID           string   `json:"run_id"`
	NextAction      string   `json:"next_action"`
	Completed       string   `json:"completed"`
	Remaining       string   `json:"remaining"`
	Risks           string   `json:"risks"`
}

func (s *Server) callTool(ctx context.Context, name string, rawArgs json.RawMessage) (string, error) {
	var args toolArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("decode arguments: %w", err)
		}
	}
	switch name {
	case "list_projects":
		return s.listProjects(ctx)
	case "project_status":
		return s.projectStatus(ctx, args)
	case "goal_show":
		return s.goalShow(ctx, args)
	case "goal_set":
		return s.goalSet(ctx, args)
	case "work_list":
		return s.workList(ctx, args)
	case "work_add":
		return s.workAdd(ctx, args)
	case "work_set_status":
		return s.workSetStatus(ctx, args)
	case "approvals_list":
		return marshal(s.store.ListAllPendingApprovals(ctx))
	case "approval_request":
		return s.approvalRequest(ctx, args)
	case "approval_decide":
		return s.approvalDecide(ctx, args)
	case "usage_report":
		return s.usageReport(ctx, args)
	case "runs_recent":
		return s.runsRecent(ctx, args)
	case "run_detail":
		return s.runDetail(ctx, args)
	case "continue_enqueue":
		return s.continueEnqueue(ctx, args)
	case "checkpoint_create":
		return s.checkpointCreate(ctx, args)
	default:
		return "", fmt.Errorf("%w: %s", errUnknownTool, name)
	}
}

func marshal[T any](value T, err error) (string, error) {
	if err != nil {
		return "", err
	}
	raw, marshalErr := json.MarshalIndent(value, "", "  ")
	if marshalErr != nil {
		return "", marshalErr
	}
	return string(raw), nil
}

func (s *Server) resolveProject(ctx context.Context, ref string) (model.Project, error) {
	if ref != "" {
		if project, err := s.store.ProjectByID(ctx, ref); err == nil {
			return project, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return model.Project{}, err
		}
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return model.Project{}, err
	}
	if ref == "" {
		if len(projects) == 1 {
			return projects[0], nil
		}
		return model.Project{}, fmt.Errorf("project is required (registered: %s)", projectNames(projects))
	}
	for _, project := range projects {
		if project.Name == ref {
			return project, nil
		}
	}
	return model.Project{}, fmt.Errorf("project %q not found (registered: %s)", ref, projectNames(projects))
}

func projectNames(projects []model.Project) string {
	names := make([]string, 0, len(projects))
	for _, project := range projects {
		names = append(names, project.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func (s *Server) currentGoal(ctx context.Context, projectID string) (model.Goal, error) {
	goal, err := s.store.CurrentGoal(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		return s.store.LatestGoal(ctx, projectID)
	}
	return goal, err
}

func (s *Server) listProjects(ctx context.Context) (string, error) {
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	type view struct {
		ID, Name, Provider, Model, State string
		GoalTitle                        string
		Progress                         float64
		Complete                         bool
	}
	result := make([]view, 0, len(projects))
	for _, project := range projects {
		entry := view{ID: project.ID, Name: project.Name, Provider: project.Provider, Model: project.Model, State: project.State}
		if goal, goalErr := s.currentGoal(ctx, project.ID); goalErr == nil {
			entry.GoalTitle = goal.Title
			entry.Progress, entry.Complete, _ = s.store.GoalProgress(ctx, goal)
		}
		result = append(result, entry)
	}
	return marshal(result, nil)
}

func (s *Server) projectStatus(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	status := map[string]any{"project": project}
	if goal, goalErr := s.currentGoal(ctx, project.ID); goalErr == nil {
		status["goal"] = goal
		progress, complete, progressErr := s.store.GoalProgress(ctx, goal)
		if progressErr != nil {
			return "", progressErr
		}
		status["progress_percent"] = progress
		status["complete"] = complete
		if criteria, criteriaErr := s.store.CriteriaStatus(ctx, goal); criteriaErr == nil {
			status["criteria"] = criteria
		}
	} else if !errors.Is(goalErr, store.ErrNotFound) {
		return "", goalErr
	}
	metrics, err := s.store.ProjectMetrics(ctx, project.ID)
	if err != nil {
		return "", err
	}
	status["metrics"] = metrics
	return marshal(status, nil)
}

func (s *Server) goalShow(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	return marshal(s.currentGoal(ctx, project.ID))
}

func (s *Server) goalSet(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	if args.Title == "" || args.Objective == "" {
		return "", errors.New("title and objective are required")
	}
	criteria := make([]model.Criterion, 0, len(args.Criteria))
	for _, raw := range args.Criteria {
		key, value, found := strings.Cut(raw, "=")
		if !found || key == "" || value == "" {
			return "", fmt.Errorf("criterion %q must be type=value", raw)
		}
		criteria = append(criteria, model.Criterion{Type: strings.TrimSpace(key), ExpectedValue: strings.TrimSpace(value)})
	}
	return marshal(s.store.SetGoal(ctx, project.ID, args.Title, args.Objective, args.Reason, criteria))
}

func (s *Server) workList(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	goal, err := s.currentGoal(ctx, project.ID)
	if err != nil {
		return "", err
	}
	items, err := s.store.ListWorkItems(ctx, goal.ID)
	if err != nil {
		return "", err
	}
	scores, err := s.store.IdeaScoresForGoal(ctx, goal.ID)
	if err != nil {
		return "", err
	}
	type view struct {
		model.WorkItem
		Score *model.IdeaScore `json:",omitempty"`
	}
	result := make([]view, 0, len(items))
	for _, item := range items {
		entry := view{WorkItem: item}
		if score, ok := scores[item.ID]; ok {
			entry.Score = &score
		}
		result = append(result, entry)
	}
	return marshal(result, nil)
}

func (s *Server) workAdd(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	goal, err := s.store.CurrentGoal(ctx, project.ID)
	if err != nil {
		return "", fmt.Errorf("active goal required: %w", err)
	}
	if args.Title == "" {
		return "", errors.New("title is required")
	}
	workType := args.Type
	if workType == "" {
		workType = "IMPLEMENT"
	}
	return marshal(s.store.CreateWorkItem(ctx, model.WorkItem{GoalID: goal.ID, Type: workType, Title: args.Title, Priority: args.Priority, ChangeScope: args.Scope, Weight: 1, EstimatedTokens: args.EstimatedTokens}))
}

func (s *Server) workSetStatus(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	if !triageStatuses[args.Status] {
		return "", errors.New("status must be one of APPROVED, BLOCKED, DISCARDED, BACKLOG")
	}
	goal, err := s.store.CurrentGoal(ctx, project.ID)
	if err != nil {
		return "", fmt.Errorf("active goal required: %w", err)
	}
	if err = s.store.SetWorkItemStatus(ctx, goal.ID, args.WorkItemID, args.Status); err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"work_item":%q,"status":%q}`, args.WorkItemID, args.Status), nil
}

func (s *Server) approvalRequest(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	actions := map[string]string{
		"protected-files": store.ApprovalProtectedFiles, store.ApprovalProtectedFiles: store.ApprovalProtectedFiles,
		"merge-branch": store.ApprovalMergeBranch, store.ApprovalMergeBranch: store.ApprovalMergeBranch,
		"publish-branch": store.ApprovalPublishBranch, store.ApprovalPublishBranch: store.ApprovalPublishBranch,
	}
	actionType, ok := actions[args.Action]
	if !ok {
		return "", fmt.Errorf("unsupported approval action %q", args.Action)
	}
	if args.Reason == "" {
		return "", errors.New("reason is required")
	}
	return marshal(s.store.RequestApproval(ctx, project.ID, actionType, args.Reason))
}

func (s *Server) approvalDecide(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	switch args.Decision {
	case "approve":
		err = s.store.Approve(ctx, project.ID, args.ApprovalID)
	case "reject":
		err = s.store.RejectApproval(ctx, project.ID, args.ApprovalID)
	default:
		return "", errors.New("decision must be approve or reject")
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"approval":%q,"decision":%q}`, args.ApprovalID, args.Decision), nil
}

func (s *Server) usageReport(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	metrics, err := s.store.ProjectMetrics(ctx, project.ID)
	if err != nil {
		return "", err
	}
	report := map[string]any{"metrics": metrics}
	if budget, budgetErr := s.store.ProjectBudgetUsage(ctx, project.ID); budgetErr == nil {
		report["budget"] = budget
	} else if !errors.Is(budgetErr, store.ErrNotFound) {
		return "", budgetErr
	}
	quotas, err := s.store.ListQuotaWindows(ctx, project.Provider)
	if err != nil {
		return "", err
	}
	report["quota_windows"] = quotas
	if series, seriesErr := s.store.DailyUsageSeries(ctx, project.ID, 14); seriesErr == nil {
		report["daily_series"] = series
	}
	return marshal(report, nil)
}

func (s *Server) runsRecent(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	return marshal(s.store.ListRecentRuns(ctx, project.ID, limit))
}

func (s *Server) runDetail(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	runs, err := s.store.ListRecentRuns(ctx, project.ID, 1000)
	if err != nil {
		return "", err
	}
	detail := map[string]any{}
	found := false
	for _, run := range runs {
		if run.ID == args.RunID {
			detail["run"], found = run, true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("run %q not found", args.RunID)
	}
	if prompt, promptErr := s.store.PromptForRun(ctx, args.RunID); promptErr == nil {
		preview := prompt.RedactedPrompt
		if len(preview) > 2000 {
			preview = preview[:2000] + "…"
		}
		detail["prompt"] = map[string]string{"template": prompt.Template, "sha256": prompt.RenderedHash, "preview": preview}
	} else if !errors.Is(promptErr, store.ErrNotFound) {
		return "", promptErr
	}
	usage, err := s.store.RunUsage(ctx, args.RunID)
	if err != nil {
		return "", err
	}
	detail["usage"] = usage
	if verifications, verifyErr := s.store.VerificationsForRun(ctx, args.RunID); verifyErr == nil && len(verifications) > 0 {
		detail["verifications"] = verifications
	}
	if changes, changesErr := s.store.ListRunFileChanges(ctx, args.RunID); changesErr == nil && len(changes) > 0 {
		detail["file_changes"] = changes
	}
	if commit, commitErr := s.store.RunCommitByRun(ctx, args.RunID); commitErr == nil {
		detail["commit"] = commit
	} else if !errors.Is(commitErr, store.ErrNotFound) {
		return "", commitErr
	}
	return marshal(detail, nil)
}

func (s *Server) continueEnqueue(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	job, err := s.store.ScheduleRecurringJob(ctx, store.SchedulerJob{ProjectID: project.ID, Type: "CONTINUE", RunAt: time.Now().UTC(), IdempotencyKey: "continue:" + project.ID})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"job":%q,"status":%q,"note":"run `+"`goalforge worker`"+` to process it"}`, job.ID, job.Status), nil
}

func (s *Server) checkpointCreate(ctx context.Context, args toolArgs) (string, error) {
	project, err := s.resolveProject(ctx, args.Project)
	if err != nil {
		return "", err
	}
	if args.NextAction == "" {
		return "", errors.New("next_action is required")
	}
	goal, err := s.currentGoal(ctx, project.ID)
	if err != nil {
		return "", err
	}
	snapshot, err := (gitops.GitInspector{}).Snapshot(ctx, project.RepositoryPath)
	if err != nil {
		return "", err
	}
	checkpoint := store.Checkpoint{ProjectID: project.ID, GoalVersion: goal.Version, Provider: project.Provider, Model: project.Model, CommitSHA: snapshot.CommitSHA, Branch: snapshot.Branch, DirtyFiles: snapshot.DirtyFiles, DirtyFingerprint: snapshot.DirtyFingerprint, CompletedSummary: args.Completed, RemainingSteps: args.Remaining, NextAction: args.NextAction, RiskSummary: args.Risks}
	if session, sessionErr := s.store.ActiveSession(ctx, project.ID, project.Provider); sessionErr == nil {
		checkpoint.SessionID = session.SessionID
	} else if !errors.Is(sessionErr, store.ErrNotFound) {
		return "", sessionErr
	}
	created, err := s.store.CreateCheckpoint(ctx, checkpoint)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"checkpoint":%q,"continuity":%q}`, created.ID, s.store.ContinuityPath(project.ID)), nil
}
