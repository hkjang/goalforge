// Package tui renders GoalForge's state in a terminal. It is a view over the
// same store the CLI, API, and MCP server use — a new way to look at the
// state, not a new place state lives.
package tui

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/goalforge/goalforge/internal/app"
	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// Snapshot is everything one screen refresh shows. Loading is separated from
// rendering so the views are pure functions of data: a view that reaches into
// the database while drawing cannot be tested, and a screen that recomputes
// while the user scrolls cannot be read.
type Snapshot struct {
	TakenAt   time.Time
	Projects  []ProjectRow
	Selected  int
	Goal      *model.Goal
	Progress  store.ProgressDetail
	Work      []WorkRow
	Approvals []store.PendingApproval
	Plan      *app.Plan
	Runs      []store.RunView
	// Integrity is whether the evidence and approvals on screen are the ones
	// GoalForge wrote. A dashboard that reports numbers it cannot vouch for is
	// the failure this whole program exists to avoid, so it is carried with
	// the numbers rather than left to a separate command nobody runs.
	Integrity store.IntegrityReport
	// LoadError explains why part of the snapshot is missing. It is carried
	// rather than returned so one failing section does not blank the screen.
	LoadError string
}

// ProjectRow is a project as the list shows it.
type ProjectRow struct {
	Project  model.Project
	Progress float64
	Complete bool
	// CriteriaMet and CriteriaTotal say how much of "done" is actually proven,
	// which is a different number from work progress and is the one people get
	// wrong.
	CriteriaMet, CriteriaTotal int
	PendingApprovals           int
	GoalTitle                  string
}

// WorkRow is a work item with the reason it cannot proceed, because "BACKLOG"
// on its own never explains why nothing is happening.
type WorkRow struct {
	Item     model.WorkItem
	Blockers []store.WorkItemBlocker
}

// Blocked reports whether anything stands in this item's way.
func (w WorkRow) Blocked() bool { return len(w.Blockers) > 0 }

// Loader reads snapshots. It is an interface so the model can be driven in
// tests without a database, and so the TUI cannot reach past it into state it
// has no business touching.
type Loader interface {
	Load(ctx context.Context, selected int) (Snapshot, error)
	Approve(ctx context.Context, projectID, approvalID string) error
	Reject(ctx context.Context, projectID, approvalID, category, note string) error
}

// StoreLoader loads snapshots from the state database.
type StoreLoader struct{ DB *store.Store }

func (l StoreLoader) Load(ctx context.Context, selected int) (Snapshot, error) {
	snapshot := Snapshot{TakenAt: time.Now(), Selected: selected}
	projects, err := l.DB.ListProjects(ctx)
	if err != nil {
		return snapshot, err
	}
	pending, err := l.DB.ListAllPendingApprovals(ctx)
	if err != nil {
		return snapshot, err
	}
	if report, integrityErr := l.DB.VerifyIntegrity(ctx); integrityErr == nil {
		snapshot.Integrity = report
	}
	pendingByProject := map[string]int{}
	for _, approval := range pending {
		pendingByProject[approval.ProjectID]++
	}
	for _, project := range projects {
		row := ProjectRow{Project: project, PendingApprovals: pendingByProject[project.ID]}
		if goal, goalErr := l.goalFor(ctx, project.ID); goalErr == nil {
			row.GoalTitle = goal.Title
			if detail, detailErr := l.DB.GoalProgressDetail(ctx, goal); detailErr == nil {
				row.Progress, row.Complete = detail.Percent, detail.Complete
				row.CriteriaTotal = len(detail.Criteria)
				for _, criterion := range detail.Criteria {
					if criterion.Satisfied {
						row.CriteriaMet++
					}
				}
			}
		}
		snapshot.Projects = append(snapshot.Projects, row)
	}
	sort.SliceStable(snapshot.Projects, func(i, j int) bool {
		return snapshot.Projects[i].Project.Name < snapshot.Projects[j].Project.Name
	})
	if len(snapshot.Projects) == 0 {
		return snapshot, nil
	}
	if snapshot.Selected < 0 || snapshot.Selected >= len(snapshot.Projects) {
		snapshot.Selected = 0
	}
	project := snapshot.Projects[snapshot.Selected].Project
	for _, approval := range pending {
		if approval.ProjectID == project.ID {
			snapshot.Approvals = append(snapshot.Approvals, approval)
		}
	}
	goal, err := l.goalFor(ctx, project.ID)
	if err != nil {
		// A project with no goal is a normal state, not a failure: it is what
		// a freshly registered project looks like.
		if errors.Is(err, store.ErrNotFound) {
			return snapshot, nil
		}
		snapshot.LoadError = err.Error()
		return snapshot, nil
	}
	snapshot.Goal = &goal
	if detail, detailErr := l.DB.GoalProgressDetail(ctx, goal); detailErr == nil {
		snapshot.Progress = detail
	} else {
		snapshot.LoadError = detailErr.Error()
	}
	items, err := l.DB.SearchWorkItems(ctx, goal.ID, store.WorkItemQuery{Limit: 200})
	if err != nil {
		snapshot.LoadError = err.Error()
		return snapshot, nil
	}
	for _, item := range items {
		row := WorkRow{Item: item}
		if detail, detailErr := l.DB.WorkItemDetails(ctx, project.ID, goal.ID, item.ID); detailErr == nil {
			row.Blockers = detail.Blockers
		}
		snapshot.Work = append(snapshot.Work, row)
	}
	if runs, runErr := l.DB.ListRecentRuns(ctx, project.ID, 8); runErr == nil {
		snapshot.Runs = runs
	}
	if plan, planErr := app.BuildPlan(ctx, l.DB, project); planErr == nil {
		snapshot.Plan = &plan
	}
	return snapshot, nil
}

func (l StoreLoader) goalFor(ctx context.Context, projectID string) (model.Goal, error) {
	goal, err := l.DB.CurrentGoal(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		return l.DB.LatestGoal(ctx, projectID)
	}
	return goal, err
}

func (l StoreLoader) Approve(ctx context.Context, projectID, approvalID string) error {
	return l.DB.Approve(ctx, projectID, approvalID)
}

func (l StoreLoader) Reject(ctx context.Context, projectID, approvalID, category, note string) error {
	return l.DB.RejectApprovalWithReason(ctx, projectID, approvalID, category, note)
}
