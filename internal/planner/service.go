package planner

import (
	"context"
	"errors"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

type Service struct {
	store  *store.Store
	policy Policy
}

func NewService(s *store.Store, p Policy) (*Service, error) {
	if s == nil {
		return nil, errors.New("store is required")
	}
	if p.MaxNewIdeas <= 0 || p.MaxUnimplemented <= 0 {
		return nil, errors.New("idea limits must be positive")
	}
	return &Service{store: s, policy: p}, nil
}

func (s *Service) DiscoverAndStore(ctx context.Context, goalID string, candidates []Candidate) (DiscoveryResult, error) {
	existing, err := s.store.ListWorkItems(ctx, goalID)
	if err != nil {
		return DiscoveryResult{}, err
	}
	result, err := Discover(s.policy, existing, candidates)
	if err != nil {
		return result, err
	}
	persisted := make([]Accepted, 0, len(result.Accepted))
	for _, accepted := range result.Accepted {
		work := model.WorkItem{GoalID: goalID, Type: "IDEA", Title: accepted.Candidate.Title, Status: accepted.Status, Risk: accepted.Candidate.Risk, ChangeScope: accepted.Candidate.ExpectedChangeScope, Weight: 1}
		created, createErr := s.store.CreateScoredIdea(ctx, work, accepted.Score)
		if createErr != nil {
			return result, createErr
		}
		accepted.Status = created.Status
		persisted = append(persisted, accepted)
	}
	result.Accepted = persisted
	return result, nil
}
func (s *Service) CanDiscover(ctx context.Context, goalID string) error {
	count, err := s.store.CountUnimplemented(ctx, goalID)
	if err != nil {
		return err
	}
	if count >= s.policy.MaxUnimplemented {
		return ErrImplementationPreferred
	}
	return nil
}
func (s *Service) SelectNext(ctx context.Context, goalID string) (model.WorkItem, error) {
	return s.store.ClaimNextWorkItem(ctx, goalID)
}
