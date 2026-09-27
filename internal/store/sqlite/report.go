package sqlite

import (
	"context"
	"time"
)

// ActivityReport summarizes a window of automated work in the terms a person
// coming back to it needs: what finished, what is stuck, what is waiting on
// them, and what it cost.
type ActivityReport struct {
	Since, Until  time.Time
	Projects      []ProjectActivity
	Runs          int64
	Tokens        int64
	CostUSD       float64
	WorkCompleted int64
	Unresolved    []UnresolvedItem
	Approvals     []PendingApproval
	// Effect measures what the automation actually delivered against what it
	// cost in rework and human attention.
	Effect AutomationEffect
}

// AutomationEffect separates verified delivery from the work spent getting
// there. A run count on its own says nothing about whether the automation is
// helping.
type AutomationEffect struct {
	VerifiedRuns, RepairRuns   int64
	ReworkRate                 float64
	Takeovers, ApprovalsNeeded int64
	MedianApprovalWaitSeconds  float64
	BlockedForUser             int64
}

type ProjectActivity struct {
	ProjectID, Name, State string
	Runs, Tokens           int64
	CostUSD                float64
	WorkCompleted          int64
	GoalTitle              string
	ProgressPercent        float64
}

// UnresolvedItem is something that stopped and is waiting for a decision,
// with the classification that explains why it stopped.
type UnresolvedItem struct {
	ProjectID, ProjectName, RunID, WorkItemID string
	State, FailureKind, Decision, Reason      string
	At                                        time.Time
}

// Activity builds the report for runs started in [since, now].
func (s *Store) Activity(ctx context.Context, since time.Time) (ActivityReport, error) {
	report := ActivityReport{Since: since.UTC(), Until: time.Now().UTC()}
	start := report.Since.Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.name,p.state,
COUNT(DISTINCT r.id),
COALESCE(SUM(CASE WHEN l.token_type<>'cost_usd' THEN l.amount ELSE 0 END),0),
COALESCE(SUM(l.cost),0)
FROM projects p LEFT JOIN runs r ON r.project_id=p.id AND r.started_at>=?
LEFT JOIN usage_ledger l ON l.run_id=r.id
GROUP BY p.id,p.name,p.state ORDER BY p.name`, start)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var activity ProjectActivity
		if err = rows.Scan(&activity.ProjectID, &activity.Name, &activity.State, &activity.Runs, &activity.Tokens, &activity.CostUSD); err != nil {
			return report, err
		}
		report.Projects = append(report.Projects, activity)
	}
	if err = rows.Err(); err != nil {
		return report, err
	}
	for i, activity := range report.Projects {
		goal, goalErr := s.CurrentGoal(ctx, activity.ProjectID)
		if goalErr != nil {
			goal, goalErr = s.LatestGoal(ctx, activity.ProjectID)
		}
		if goalErr == nil {
			report.Projects[i].GoalTitle = goal.Title
			progress, _, progressErr := s.GoalProgress(ctx, goal)
			if progressErr != nil {
				return report, progressErr
			}
			report.Projects[i].ProgressPercent = progress
			// Work completed is counted from the commits verified in the
			// window, because a work item's status only shows where it ended
			// up, not when it got there.
			if err = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT work_item_id) FROM run_commits WHERE project_id=? AND created_at>=?`,
				activity.ProjectID, start).Scan(&report.Projects[i].WorkCompleted); err != nil {
				return report, err
			}
		}
		report.Runs += activity.Runs
		report.Tokens += activity.Tokens
		report.CostUSD += activity.CostUSD
		report.WorkCompleted += report.Projects[i].WorkCompleted
	}
	unresolved, err := s.db.QueryContext(ctx, `SELECT r.project_id,p.name,r.id,COALESCE(r.work_item_id,''),r.state,
COALESCE(a.failure_kind,''),COALESCE(a.decision,''),COALESCE(a.reason,''),r.started_at
FROM runs r JOIN projects p ON p.id=r.project_id
LEFT JOIN repair_attempts a ON a.run_id=r.id
WHERE r.started_at>=? AND r.state IN ('REPAIR_REQUIRED','FAILED')
ORDER BY r.started_at DESC LIMIT 50`, start)
	if err != nil {
		return report, err
	}
	defer unresolved.Close()
	for unresolved.Next() {
		var item UnresolvedItem
		var at string
		if err = unresolved.Scan(&item.ProjectID, &item.ProjectName, &item.RunID, &item.WorkItemID, &item.State,
			&item.FailureKind, &item.Decision, &item.Reason, &at); err != nil {
			return report, err
		}
		item.At, _ = time.Parse(time.RFC3339Nano, at)
		report.Unresolved = append(report.Unresolved, item)
	}
	if err = unresolved.Err(); err != nil {
		return report, err
	}
	if report.Effect, err = s.automationEffect(ctx, start); err != nil {
		return report, err
	}
	report.Approvals, err = s.ListAllPendingApprovals(ctx)
	return report, err
}

func (s *Store) automationEffect(ctx context.Context, start string) (AutomationEffect, error) {
	var effect AutomationEffect
	if err := s.db.QueryRowContext(ctx, `SELECT
COALESCE(SUM(CASE WHEN state IN ('CHECKPOINTING','COMPLETED') THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN state IN ('REPAIR_REQUIRED','FAILED') THEN 1 ELSE 0 END),0)
FROM runs WHERE started_at>=?`, start).Scan(&effect.VerifiedRuns, &effect.RepairRuns); err != nil {
		return effect, err
	}
	if total := effect.VerifiedRuns + effect.RepairRuns; total > 0 {
		effect.ReworkRate = float64(effect.RepairRuns) / float64(total) * 100
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM takeovers WHERE taken_at>=?`, start).Scan(&effect.Takeovers); err != nil {
		return effect, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE requested_at>=?`, start).Scan(&effect.ApprovalsNeeded); err != nil {
		return effect, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repair_attempts WHERE created_at>=? AND decision<>?`, start, RepairRetry).Scan(&effect.BlockedForUser); err != nil {
		return effect, err
	}
	// The median rather than the mean: one approval left overnight should not
	// be reported as the typical wait.
	rows, err := s.db.QueryContext(ctx, `SELECT (julianday(approved_at)-julianday(requested_at))*86400 FROM approvals WHERE requested_at>=? AND approved_at IS NOT NULL AND approved_at<>'' ORDER BY 1`, start)
	if err != nil {
		return effect, err
	}
	defer rows.Close()
	var waits []float64
	for rows.Next() {
		var seconds float64
		if err = rows.Scan(&seconds); err != nil {
			return effect, err
		}
		if seconds >= 0 {
			waits = append(waits, seconds)
		}
	}
	if err = rows.Err(); err != nil {
		return effect, err
	}
	if len(waits) > 0 {
		effect.MedianApprovalWaitSeconds = waits[len(waits)/2]
	}
	return effect, nil
}
