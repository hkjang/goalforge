package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

type Handler func(context.Context, store.SchedulerJob) (Outcome, error)
type Outcome struct{ RescheduleAt *time.Time }

type JobStore interface {
	ClaimDueJob(context.Context, time.Time, string, time.Duration) (store.SchedulerJob, error)
	CompleteJob(context.Context, string, string) error
	FailJob(context.Context, string, string, string) error
	RescheduleJob(context.Context, string, string, time.Time, string) error
}

type Scheduler struct {
	store         JobStore
	owner         string
	leaseDuration time.Duration
	handlers      map[string]Handler
}

func New(s JobStore, owner string, leaseDuration time.Duration) (*Scheduler, error) {
	if s == nil || owner == "" || leaseDuration <= 0 {
		return nil, errors.New("store, owner, and positive lease duration are required")
	}
	return &Scheduler{store: s, owner: owner, leaseDuration: leaseDuration, handlers: make(map[string]Handler)}, nil
}

func (s *Scheduler) Handle(jobType string, handler Handler) error {
	if jobType == "" || handler == nil {
		return errors.New("job type and handler are required")
	}
	if _, exists := s.handlers[jobType]; exists {
		return fmt.Errorf("handler for %s already registered", jobType)
	}
	s.handlers[jobType] = handler
	return nil
}

func (s *Scheduler) RunOne(ctx context.Context, now time.Time) (bool, error) {
	job, err := s.store.ClaimDueJob(ctx, now, s.owner, s.leaseDuration)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	handler := s.handlers[job.Type]
	if handler == nil {
		message := "no handler registered for " + job.Type
		return true, errors.Join(errors.New(message), s.store.FailJob(ctx, job.ID, s.owner, message))
	}
	outcome, handlerErr := handler(ctx, job)
	if handlerErr != nil {
		if outcome.RescheduleAt != nil {
			return true, errors.Join(handlerErr, s.store.RescheduleJob(ctx, job.ID, s.owner, *outcome.RescheduleAt, handlerErr.Error()))
		}
		return true, errors.Join(handlerErr, s.store.FailJob(ctx, job.ID, s.owner, handlerErr.Error()))
	}
	if outcome.RescheduleAt != nil {
		return true, s.store.RescheduleJob(ctx, job.ID, s.owner, *outcome.RescheduleAt, "")
	}
	return true, s.store.CompleteJob(ctx, job.ID, s.owner)
}
