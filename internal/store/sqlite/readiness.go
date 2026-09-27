package sqlite

import (
	"context"
	"errors"

	"github.com/goalforge/goalforge/internal/diagnostics"
)

// ReadinessInput gathers what readiness checking needs about a project: its
// goal, criteria, gates, budget, and whether its evidence is current.
func (s *Store) ReadinessInput(ctx context.Context, projectID string) (diagnostics.ReadinessInput, error) {
	input := diagnostics.ReadinessInput{HasProject: true}
	goal, err := s.CurrentGoal(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		goal, err = s.LatestGoal(ctx, projectID)
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return input, nil
	case err != nil:
		return input, err
	}
	input.GoalTitle = goal.Title
	for _, criterion := range goal.Criteria {
		input.Criteria = append(input.Criteria, criterion.Type)
	}
	statuses, err := s.CriteriaStatus(ctx, goal)
	if err != nil {
		return input, err
	}
	for _, status := range statuses {
		if status.Status == "STALE" {
			input.StaleCriteria = append(input.StaleCriteria, status.Type)
		}
	}
	gates, err := s.ListGates(ctx, projectID)
	if err != nil {
		return input, err
	}
	for _, gate := range gates {
		input.Gates = append(input.Gates, diagnostics.GateSpec{Type: gate.Type, Command: gate.Command, Required: gate.Required})
	}
	budget, err := s.ProjectBudgetConfig(ctx, projectID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return input, err
	}
	input.BudgetConfigured = budget.TokenLimit > 0 || budget.CostLimitUSD > 0 || budget.DailyRunLimit > 0
	integration, err := s.IntegrationStatus(ctx, projectID)
	if err != nil {
		return input, err
	}
	input.IntegrationPending, input.IntegrationReason = integration.Pending, integration.Reason
	return input, nil
}
