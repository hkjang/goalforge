package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/rrsi"
)

// AppliedChange is one configuration setting automation altered.
//
// The row exists so the change can be put back. An automatic change with no
// record of what it replaced is one nobody can undo, and a configuration that
// cannot be undone drifts a little further on every failed experiment.
type AppliedChange struct {
	ID, ProjectID, Component, Field string
	From, To                        string
	// ProposalID is the recorded proposal this change came from, when it came
	// from one. A change a person made by hand has none, and requiring one
	// would stop them making it.
	ProposalID string
	Round      int
	AppliedAt  time.Time
	// Settled is false while the change is still being measured. Reverted and
	// Outcome say how it ended.
	Settled   bool
	Reverted  bool
	Outcome   string
	SettledAt time.Time
}

// ErrChangeOutstanding means a change is already in flight for this project.
//
// Two changes applied before either is measured make the measurement
// unattributable: the score moved, and nothing says which of them moved it.
// That is the edit budget's rule, enforced where it actually matters.
var ErrChangeOutstanding = errors.New("아직 판정되지 않은 변경이 있습니다")

// ApplyChange alters a setting and records what it replaced.
func (s *Store) ApplyChange(ctx context.Context, projectID string, round int, component string, change rrsi.Change) (AppliedChange, error) {
	return s.ApplyChangeFor(ctx, projectID, "", round, component, change)
}

// ApplyChangeFor applies a change and remembers which proposal asked for it,
// so settling the change can tell the proposal what the measurement found.
//
// The link is what closes the loop. Applying a change without it leaves the
// proposal recorded as pending forever, and a history of pending proposals
// answers no question the next round asks.
func (s *Store) ApplyChangeFor(ctx context.Context, projectID, proposalID string, round int, component string, change rrsi.Change) (AppliedChange, error) {
	var applied AppliedChange
	if err := change.Validate(component); err != nil {
		return applied, err
	}
	outstanding, err := s.OutstandingChange(ctx, projectID)
	switch {
	case err == nil:
		return applied, fmt.Errorf("%w: %s 를 %s 에서 %s 로 바꾼 것이 아직 판정되지 않았습니다",
			ErrChangeOutstanding, outstanding.Field, outstanding.From, outstanding.To)
	case !errors.Is(err, ErrNotFound):
		return applied, err
	}
	// The current value is read here rather than trusted from the proposal.
	// A proposer's idea of the current value is a claim; the database holds
	// the fact, and reverting to a wrong "from" is worse than not reverting.
	current, err := s.currentSetting(ctx, projectID, change.Field)
	if err != nil {
		return applied, err
	}
	if current != change.From {
		return applied, fmt.Errorf("%s 의 현재 값은 %q 인데 제안은 %q 에서 바꾼다고 합니다 — 그 사이에 누군가 바꿨습니다",
			change.Field, current, change.From)
	}
	if err = s.setSetting(ctx, projectID, change.Field, change.To); err != nil {
		return applied, err
	}
	applied = AppliedChange{ID: NewID("CHG"), ProjectID: projectID, Component: component,
		Field: change.Field, From: current, To: change.To, ProposalID: proposalID,
		Round: round, AppliedAt: time.Now().UTC()}
	_, err = s.db.ExecContext(ctx, `INSERT INTO applied_changes(id,project_id,component,field,from_value,to_value,proposal_id,round,applied_at,settled,reverted,outcome,settled_at)
VALUES(?,?,?,?,?,?,?,?,?,0,0,'','')`,
		applied.ID, applied.ProjectID, applied.Component, applied.Field, applied.From, applied.To,
		applied.ProposalID, applied.Round, applied.AppliedAt.Format(time.RFC3339Nano))
	return applied, err
}

// OutstandingChange is the change still waiting to be judged.
func (s *Store) OutstandingChange(ctx context.Context, projectID string) (AppliedChange, error) {
	var change AppliedChange
	var appliedAt string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,component,field,from_value,to_value,round,applied_at
FROM applied_changes WHERE project_id=? AND settled=0 ORDER BY applied_at DESC LIMIT 1`, projectID).
		Scan(&change.ID, &change.ProjectID, &change.Component, &change.Field, &change.From, &change.To,
			&change.Round, &appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return change, ErrNotFound
	}
	if err == nil {
		change.AppliedAt, _ = time.Parse(time.RFC3339Nano, appliedAt)
	}
	return change, err
}

// SettleChange closes out an applied change, putting the setting back when the
// measurement did not support it.
//
// Reverting is not optional on a verdict that did not support the change. A
// change left in place after failing to show an improvement is the
// configuration drifting on its failures, one experiment at a time.
func (s *Store) SettleChange(ctx context.Context, changeID string, settlement Settlement) (AppliedChange, error) {
	var change AppliedChange
	var appliedAt string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,component,field,from_value,to_value,proposal_id,round,applied_at
FROM applied_changes WHERE id=? AND settled=0`, changeID).
		Scan(&change.ID, &change.ProjectID, &change.Component, &change.Field, &change.From, &change.To,
			&change.ProposalID, &change.Round, &appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return change, ErrNotFound
	}
	if err != nil {
		return change, err
	}
	change.AppliedAt, _ = time.Parse(time.RFC3339Nano, appliedAt)
	keep := settlement.Verdict == rrsi.VerdictBetter || settlement.Verdict == rrsi.VerdictNoWorse
	if !keep {
		if err = s.setSetting(ctx, change.ProjectID, change.Field, change.From); err != nil {
			return change, err
		}
		change.Reverted = true
	}
	change.Settled, change.SettledAt = true, time.Now().UTC()
	change.Outcome = settlement.Verdict + ": " + settlement.Detail
	reverted := 0
	if change.Reverted {
		reverted = 1
	}
	// The row is kept rather than deleted. The history is what stops the same
	// change being proposed again, and a reverted change that left no trace is
	// one the next round will cheerfully repeat.
	if _, err = s.db.ExecContext(ctx, `UPDATE applied_changes SET settled=1,reverted=?,outcome=?,settled_at=? WHERE id=?`,
		reverted, change.Outcome, change.SettledAt.Format(time.RFC3339Nano), change.ID); err != nil {
		return change, err
	}
	// And the proposal that asked for it learns what happened. A change kept
	// is the hypothesis holding; a change put back is the hypothesis tested
	// and found wanting, which is the only thing that keeps the next round
	// from proposing it again.
	err = s.settleProposal(ctx, change.ProposalID, rrsi.Record{Verdict: settlement.Verdict,
		ScoreDelta: settlement.ScoreDelta, CostDelta: settlement.CostDelta,
		Score: settlement.Score, Accepted: keep})
	return change, err
}

// Settlement is what the measurement established about an applied change.
//
// The numbers travel with the verdict because they are what the next round
// reads. A verdict on its own says the change was judged; the numbers say
// whether the run is still improving or has stalled.
type Settlement struct {
	Verdict, Detail              string
	Score, ScoreDelta, CostDelta float64
}

// AppliedChanges lists what automation has altered, newest first.
func (s *Store) AppliedChanges(ctx context.Context, projectID string) ([]AppliedChange, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,component,field,from_value,to_value,round,applied_at,settled,reverted,outcome
FROM applied_changes WHERE project_id=? ORDER BY applied_at DESC,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []AppliedChange
	for rows.Next() {
		var change AppliedChange
		var appliedAt string
		var settled, reverted int
		if err = rows.Scan(&change.ID, &change.ProjectID, &change.Component, &change.Field, &change.From,
			&change.To, &change.Round, &appliedAt, &settled, &reverted, &change.Outcome); err != nil {
			return nil, err
		}
		change.AppliedAt, _ = time.Parse(time.RFC3339Nano, appliedAt)
		change.Settled, change.Reverted = settled == 1, reverted == 1
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

// CurrentSettingValue is currentSetting for callers outside the package, so a
// command can show what it is about to replace without guessing at it.
func (s *Store) CurrentSettingValue(ctx context.Context, projectID, field string) (string, error) {
	return s.currentSetting(ctx, projectID, field)
}

// currentSetting reads a setting's present value as a string.
func (s *Store) currentSetting(ctx context.Context, projectID, field string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "wip_limit", "model":
		// Read only what this field needs. Loading the budget as well made a
		// project that never set one unable to read its concurrency limit,
		// because a project with no budget row is an ordinary state and
		// ProjectBudgetConfig reports it as not found.
		project, err := s.ProjectByID(ctx, projectID)
		if err != nil {
			return "", err
		}
		if field == "model" {
			return project.Model, nil
		}
		return strconv.Itoa(project.WIPLimit), nil
	}
	budget, err := s.budgetOrZero(ctx, projectID)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "daily_run_limit":
		return strconv.FormatInt(budget.DailyRunLimit, 10), nil
	case "daily_token_limit":
		return strconv.FormatInt(budget.DailyTokenLimit, 10), nil
	case "daily_cost_limit":
		return strconv.FormatFloat(budget.DailyCostLimitUSD, 'f', -1, 64), nil
	}
	return "", fmt.Errorf("%q 는 자동으로 바꿀 수 있는 설정이 아닙니다", field)
}

// budgetOrZero reads a project's budget, treating "never set" as all zeros.
//
// A project with no budget row has no limits, which is a value and not an
// error. Reporting it as not found would stop automation reading any budget
// setting on exactly the projects that have not configured one.
func (s *Store) budgetOrZero(ctx context.Context, projectID string) (ProjectBudget, error) {
	budget, err := s.ProjectBudgetConfig(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		return ProjectBudget{}, nil
	}
	return budget, err
}

// setSetting writes a setting.
func (s *Store) setSetting(ctx context.Context, projectID, field, value string) error {
	budget, err := s.budgetOrZero(ctx, projectID)
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "wip_limit":
		limit, convErr := strconv.Atoi(value)
		if convErr != nil || limit < 1 {
			return fmt.Errorf("동시 실행 한도는 1 이상의 정수여야 합니다: %q", value)
		}
		return s.SetWIPLimit(ctx, projectID, limit)
	case "model":
		project, projErr := s.ProjectByID(ctx, projectID)
		if projErr != nil {
			return projErr
		}
		_, projErr = s.SwitchProvider(ctx, projectID, project.Provider, value, "자동 구성 실험")
		return projErr
	case "daily_run_limit":
		limit, convErr := strconv.ParseInt(value, 10, 64)
		if convErr != nil || limit < 0 {
			return fmt.Errorf("일일 실행 한도는 0 이상의 정수여야 합니다: %q", value)
		}
		return s.SetDailyLimits(ctx, projectID, limit, budget.DailyTokenLimit, budget.DailyCostLimitUSD)
	case "daily_token_limit":
		limit, convErr := strconv.ParseInt(value, 10, 64)
		if convErr != nil || limit < 0 {
			return fmt.Errorf("일일 토큰 한도는 0 이상의 정수여야 합니다: %q", value)
		}
		return s.SetDailyLimits(ctx, projectID, budget.DailyRunLimit, limit, budget.DailyCostLimitUSD)
	case "daily_cost_limit":
		limit, convErr := strconv.ParseFloat(value, 64)
		if convErr != nil || limit < 0 {
			return fmt.Errorf("일일 비용 한도는 0 이상이어야 합니다: %q", value)
		}
		return s.SetDailyLimits(ctx, projectID, budget.DailyRunLimit, budget.DailyTokenLimit, limit)
	}
	return fmt.Errorf("%q 는 자동으로 바꿀 수 있는 설정이 아닙니다", field)
}
