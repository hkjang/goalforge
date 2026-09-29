package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Outcome confirmation states. An outcome nobody can measure is not a
// requirement, it is a wish, and the difference has to be visible before work
// starts rather than discovered at completion.
const (
	// OutcomeConfirmed has a named verification method and a judge.
	OutcomeConfirmed = "CONFIRMED"
	// OutcomeUnconfirmed is stated but not yet measurable.
	OutcomeUnconfirmed = "UNCONFIRMED"
)

// RequiredOutcome is one thing the goal must achieve. It carries how it will
// be judged and by whom, because "최고 수준" and "빠르게" are not outcomes
// until someone says what would settle them.
type RequiredOutcome struct {
	Key, Statement string
	// Method is how it is measured — a gate type, a review, an observation
	// window. Judge is who decides. Either being empty makes the outcome
	// unconfirmed.
	Method, Judge string
	// Metric and Threshold are set when the outcome is numeric, which is what
	// makes two outcomes comparable enough to conflict.
	Metric, Comparator, Threshold string
	Status                        string
}

// Confirmed reports whether this outcome can actually be settled.
func (o RequiredOutcome) Confirmed() bool {
	return strings.TrimSpace(o.Method) != "" && strings.TrimSpace(o.Judge) != ""
}

// GoalContract is a versioned statement of what a goal commits to. Changing it
// creates a new version with a reason and a decider; the previous version
// keeps its own outcomes, so narrowing the goal cannot turn a past failure
// into a success after the fact.
type GoalContract struct {
	ID, ProjectID, GoalID string
	Version               int
	PreviousID            string
	Title, Objective      string
	Users, Scenarios      string
	Outcomes              []RequiredOutcome
	Exclusions            []string
	Stage                 string
	BudgetTokens          int64
	BudgetUSD             float64
	Deadline              time.Time
	ChangeReason, Decider string
	CreatedAt             time.Time
}

// Unconfirmed lists the outcomes that cannot yet be settled.
func (c GoalContract) Unconfirmed() []RequiredOutcome {
	var result []RequiredOutcome
	for _, outcome := range c.Outcomes {
		if !outcome.Confirmed() {
			result = append(result, outcome)
		}
	}
	return result
}

// Conflict is a pair of required outcomes that cannot both hold. Presenting
// them is the point: dropping one silently is how a contract quietly becomes
// a different contract.
type Conflict struct {
	Left, Right RequiredOutcome
	Detail      string
}

// Conflicts finds required outcomes that contradict each other. It only claims
// a conflict it can substantiate — two numeric outcomes on the same metric
// whose thresholds cannot both be met — because a detector that guesses would
// train people to ignore it.
func (c GoalContract) Conflicts() []Conflict {
	byMetric := map[string][]RequiredOutcome{}
	for _, outcome := range c.Outcomes {
		metric := strings.TrimSpace(strings.ToLower(outcome.Metric))
		if metric == "" {
			continue
		}
		byMetric[metric] = append(byMetric[metric], outcome)
	}
	metrics := make([]string, 0, len(byMetric))
	for metric := range byMetric {
		metrics = append(metrics, metric)
	}
	sort.Strings(metrics)
	var conflicts []Conflict
	for _, metric := range metrics {
		outcomes := byMetric[metric]
		for i := 0; i < len(outcomes); i++ {
			for j := i + 1; j < len(outcomes); j++ {
				if detail, conflicting := incompatible(outcomes[i], outcomes[j]); conflicting {
					conflicts = append(conflicts, Conflict{Left: outcomes[i], Right: outcomes[j], Detail: detail})
				}
			}
		}
	}
	return conflicts
}

// incompatible reports whether two requirements on the same metric leave no
// value that satisfies both.
//
// Each requirement is read as an interval, and two requirements conflict when
// their intervals do not intersect. The earlier version compared a pair of
// "at least / at most" flags, which lost the two distinctions that matter:
// ">= 200" and "<= 200" meet at exactly 200 and are fine, while "> 200" and
// "<= 200" admit nothing — and an exact requirement was only ever compared
// against another exact one, so "오류 = 0" beside "오류 >= 1" went unreported.
func incompatible(left, right RequiredOutcome) (string, bool) {
	leftRange, leftOK := outcomeRange(left)
	rightRange, rightOK := outcomeRange(right)
	if !leftOK || !rightOK {
		return "", false
	}
	// A threshold written without a unit is taken to be in whatever unit the
	// metric is measured in, so it compares with anything. Two *different*
	// named units are a different kind of quantity, and nothing can be
	// concluded from them — a detector that concluded anyway would be guessing,
	// and a guessed conflict is how people learn to ignore the real ones.
	if leftRange.family != "" && rightRange.family != "" && leftRange.family != rightRange.family {
		return "", false
	}
	if leftRange.intersects(rightRange) {
		return "", false
	}
	return fmt.Sprintf("%s 에 대해 %s 와 %s 를 동시에 요구합니다 — 두 조건을 모두 만족하는 값이 없습니다",
		left.Metric, left.describe(), right.describe()), true
}

// interval is one requirement as the set of values that satisfy it. The
// inclusive flags are what separate a shared endpoint from an empty overlap.
type interval struct {
	low, high                   float64
	lowInclusive, highInclusive bool
	family                      string
}

func (i interval) intersects(other interval) bool {
	low, lowInclusive := i.low, i.lowInclusive
	if other.low > low || (other.low == low && !other.lowInclusive) {
		low, lowInclusive = other.low, other.lowInclusive
	}
	high, highInclusive := i.high, i.highInclusive
	if other.high < high || (other.high == high && !other.highInclusive) {
		high, highInclusive = other.high, other.highInclusive
	}
	if low > high {
		return false
	}
	if low == high {
		return lowInclusive && highInclusive
	}
	return true
}

// outcomeRange reads a requirement's comparator and threshold into an interval,
// converting the threshold into its unit family's base unit so "1s" and
// "200ms" are compared as the same quantity rather than as 1 and 200.
func outcomeRange(out RequiredOutcome) (interval, bool) {
	value, unit, ok := splitValueAndUnit(strings.TrimSpace(out.Threshold))
	if !ok {
		return interval{}, false
	}
	normalized, family := normalizeUnit(value, unit)
	span := interval{low: math.Inf(-1), high: math.Inf(1), lowInclusive: true, highInclusive: true, family: family}
	switch strings.TrimSpace(strings.ToLower(out.Comparator)) {
	case ">=", "min", "at_least":
		span.low = normalized
	case ">", "over":
		span.low, span.lowInclusive = normalized, false
	case "<=", "max", "at_most":
		span.high = normalized
	case "<", "under":
		span.high, span.highInclusive = normalized, false
	case "!=", "ne":
		// "not this value" excludes a single point, which cannot be written as
		// one interval and never makes a pair unsatisfiable on its own.
		return interval{}, false
	default:
		span.low, span.high = normalized, normalized
	}
	return span, true
}

// describe says the requirement the way it was written, so the conflict message
// names both clauses rather than making the reader open the contract.
func (out RequiredOutcome) describe() string {
	threshold := strings.TrimSpace(out.Threshold)
	switch strings.TrimSpace(strings.ToLower(out.Comparator)) {
	case ">=", "min", "at_least":
		return threshold + " 이상"
	case ">", "over":
		return threshold + " 초과"
	case "<=", "max", "at_most":
		return threshold + " 이하"
	case "<", "under":
		return threshold + " 미만"
	case "!=", "ne":
		return threshold + " 이 아님"
	}
	return threshold
}

// SaveContract records a new contract version. A change after the first needs
// a reason and a decider: a contract that can be edited anonymously is not a
// commitment, and narrowing one without a record is how a missed goal becomes
// a met one.
func (s *Store) SaveContract(ctx context.Context, contract GoalContract) (GoalContract, error) {
	if contract.ProjectID == "" || strings.TrimSpace(contract.Title) == "" {
		return contract, errors.New("project and contract title are required")
	}
	if len(contract.Outcomes) == 0 {
		return contract, errors.New("a contract needs at least one required outcome; without one nothing can judge it")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contract, err
	}
	defer tx.Rollback()
	var previousID string
	var previousVersion int
	err = tx.QueryRowContext(ctx, `SELECT id,version FROM goal_contracts WHERE project_id=? ORDER BY version DESC LIMIT 1`, contract.ProjectID).
		Scan(&previousID, &previousVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return contract, err
	}
	contract.Version = previousVersion + 1
	contract.PreviousID = previousID
	if contract.Version > 1 {
		if strings.TrimSpace(contract.ChangeReason) == "" || strings.TrimSpace(contract.Decider) == "" {
			return contract, errors.New("changing a contract needs a reason and a decider")
		}
	}
	if contract.ID == "" {
		contract.ID = NewID("CTR")
	}
	if contract.CreatedAt.IsZero() {
		contract.CreatedAt = time.Now().UTC()
	}
	deadline := ""
	if !contract.Deadline.IsZero() {
		deadline = contract.Deadline.UTC().Format(time.RFC3339Nano)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO goal_contracts(id,project_id,goal_id,version,previous_id,title,objective,users,scenarios,exclusions,stage,budget_tokens,budget_usd,deadline,change_reason,decider,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		contract.ID, contract.ProjectID, contract.GoalID, contract.Version, contract.PreviousID, contract.Title,
		contract.Objective, contract.Users, contract.Scenarios, strings.Join(contract.Exclusions, "\n"), contract.Stage,
		contract.BudgetTokens, contract.BudgetUSD, deadline, contract.ChangeReason, contract.Decider,
		contract.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return contract, err
	}
	for i := range contract.Outcomes {
		outcome := &contract.Outcomes[i]
		if strings.TrimSpace(outcome.Key) == "" {
			return contract, errors.New("every required outcome needs a key")
		}
		outcome.Status = OutcomeUnconfirmed
		if outcome.Confirmed() {
			outcome.Status = OutcomeConfirmed
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO contract_outcomes(contract_id,outcome_key,statement,method,judge,metric,comparator,threshold,status) VALUES(?,?,?,?,?,?,?,?,?)`,
			contract.ID, outcome.Key, outcome.Statement, outcome.Method, outcome.Judge,
			outcome.Metric, outcome.Comparator, outcome.Threshold, outcome.Status); err != nil {
			return contract, err
		}
	}
	return contract, tx.Commit()
}

// CurrentContract returns the latest contract version for a project.
func (s *Store) CurrentContract(ctx context.Context, projectID string) (GoalContract, error) {
	var contract GoalContract
	var exclusions, deadline string
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,goal_id,version,previous_id,title,objective,users,scenarios,exclusions,stage,budget_tokens,budget_usd,deadline,change_reason,decider,created_at FROM goal_contracts WHERE project_id=? ORDER BY version DESC LIMIT 1`, projectID).
		Scan(&contract.ID, &contract.ProjectID, &contract.GoalID, &contract.Version, &contract.PreviousID,
			&contract.Title, &contract.Objective, &contract.Users, &contract.Scenarios, &exclusions, &contract.Stage,
			&contract.BudgetTokens, &contract.BudgetUSD, &deadline, &contract.ChangeReason, &contract.Decider, new(string))
	if errors.Is(err, sql.ErrNoRows) {
		return contract, ErrNotFound
	}
	if err != nil {
		return contract, err
	}
	if exclusions != "" {
		contract.Exclusions = strings.Split(exclusions, "\n")
	}
	contract.Deadline, _ = time.Parse(time.RFC3339Nano, deadline)
	contract.Outcomes, err = s.contractOutcomes(ctx, contract.ID)
	return contract, err
}

func (s *Store) contractOutcomes(ctx context.Context, contractID string) ([]RequiredOutcome, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT outcome_key,statement,method,judge,metric,comparator,threshold,status FROM contract_outcomes WHERE contract_id=? ORDER BY outcome_key`, contractID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RequiredOutcome
	for rows.Next() {
		var outcome RequiredOutcome
		if err = rows.Scan(&outcome.Key, &outcome.Statement, &outcome.Method, &outcome.Judge,
			&outcome.Metric, &outcome.Comparator, &outcome.Threshold, &outcome.Status); err != nil {
			return nil, err
		}
		result = append(result, outcome)
	}
	return result, rows.Err()
}

// ContractHistory returns every version, oldest first. Each keeps its own
// outcomes, so what a past version required stays visible after the goal has
// been narrowed.
func (s *Store) ContractHistory(ctx context.Context, projectID string) ([]GoalContract, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,version,title,change_reason,decider,created_at FROM goal_contracts WHERE project_id=? ORDER BY version`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []GoalContract
	for rows.Next() {
		var contract GoalContract
		var created string
		if err = rows.Scan(&contract.ID, &contract.Version, &contract.Title, &contract.ChangeReason, &contract.Decider, &created); err != nil {
			return nil, err
		}
		contract.ProjectID = projectID
		contract.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, contract)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range result {
		if result[i].Outcomes, err = s.contractOutcomes(ctx, result[i].ID); err != nil {
			return nil, err
		}
	}
	return result, nil
}
