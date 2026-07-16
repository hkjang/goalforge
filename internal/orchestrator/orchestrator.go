package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/usage"
)

var ErrWaitingQuota = errors.New("provider quota requires waiting before a new run")
var ErrPreflightBlocked = errors.New("run blocked by preflight policy")
var ErrLargeWorkAtQuotaWarning = errors.New("large work deferred at quota warning threshold")
var ErrDailyLimitExceeded = errors.New("project daily execution limit exceeded")
var ErrTurnTimeout = errors.New("provider turn timed out")
var ErrRunTimeout = errors.New("orchestrator run timed out")

type Orchestrator struct {
	store                   *store.Store
	providers               map[string]provider.Provider
	inspector               gitops.Inspector
	controlPoll, drainGrace time.Duration
	quotaPolicy             usage.Policy
	largeWorkTokenThreshold int64
}

type Request struct {
	RunID, WorkItemID, Prompt    string
	OutputSchema, PromptTemplate string
	TaskType                     string
	Project                      model.Project
	WorkspaceWrite               bool
	ReadOnlyTask, Isolated       bool
	EstimatedTokens              int64
}

type Result struct {
	RunID, SessionID, State, FinalMessage string
	Resumed                               bool
	Usage                                 provider.Usage
}

func New(s *store.Store, providers ...provider.Provider) (*Orchestrator, error) {
	o := &Orchestrator{store: s, providers: make(map[string]provider.Provider), inspector: gitops.GitInspector{}, controlPoll: 500 * time.Millisecond, drainGrace: 30 * time.Second, quotaPolicy: usage.DefaultPolicy(), largeWorkTokenThreshold: 20_000}
	for _, p := range providers {
		if p == nil {
			return nil, errors.New("nil provider")
		}
		if _, exists := o.providers[p.Name()]; exists {
			return nil, fmt.Errorf("duplicate provider %s", p.Name())
		}
		o.providers[p.Name()] = p
	}
	return o, nil
}

func (o *Orchestrator) ConfigureControl(inspector gitops.Inspector, pollInterval, drainGrace time.Duration) error {
	if inspector == nil || pollInterval <= 0 || drainGrace < 0 {
		return errors.New("inspector, positive poll interval, and non-negative drain grace are required")
	}
	o.inspector, o.controlPoll, o.drainGrace = inspector, pollInterval, drainGrace
	return nil
}

func (o *Orchestrator) Run(ctx context.Context, request Request) (Result, error) {
	result := Result{RunID: request.RunID, State: "FAILED"}
	p, ok := o.providers[request.Project.Provider]
	if !ok {
		return result, fmt.Errorf("provider %q is not registered", request.Project.Provider)
	}
	if request.RunID == "" || request.Prompt == "" || request.Project.ID == "" {
		return result, errors.New("run ID, project, and prompt are required")
	}
	runtimePolicy := store.DefaultRuntimePolicy()
	if storedPolicy, policyErr := o.store.RuntimePolicy(ctx, request.Project.ID); policyErr == nil {
		runtimePolicy = storedPolicy
	} else if !errors.Is(policyErr, store.ErrNotFound) {
		return result, policyErr
	}
	runCtx, cancelRun := context.WithTimeout(ctx, runtimePolicy.RunTimeout)
	defer cancelRun()
	ctx = runCtx
	if state, preflightErr := o.preflight(ctx, p, request); preflightErr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			failErr := o.store.FailBeforeRun(context.WithoutCancel(ctx), request.Project.ID, request.WorkItemID)
			result.State = "FAILED"
			return result, errors.Join(ErrRunTimeout, runCtx.Err(), failErr)
		}
		result.State = state
		return result, preflightErr
	}
	var handoff *store.ProviderHandoff
	if !request.Isolated {
		if pending, handoffErr := o.store.PendingHandoff(ctx, request.Project.ID, p.Name()); handoffErr == nil {
			handoff = &pending
			request.Prompt += "\n\n제공자 전환 인수인계 패키지:\n" + pending.ContentJSON + "\n이전 제공자의 세션 식별자는 공유되지 않는다. 저장소 상태를 먼저 확인하고 미완료 작업부터 계속하라."
		} else if !errors.Is(handoffErr, store.ErrNotFound) {
			return result, fmt.Errorf("load provider handoff: %w", handoffErr)
		}
	}
	if err := o.store.StartRun(ctx, store.RunRecord{ID: request.RunID, ProjectID: request.Project.ID, WorkItemID: request.WorkItemID, Provider: p.Name(), Model: request.Project.Model, TaskType: request.TaskType}); err != nil {
		return result, err
	}
	if err := o.store.RecordPrompt(ctx, request.RunID, request.PromptTemplate, request.Prompt); err != nil {
		_ = o.store.FinishRun(ctx, request.RunID, "FAILED", "FAILED")
		return result, fmt.Errorf("record prompt audit: %w", err)
	}
	runRequest := provider.RunRequest{RunID: request.RunID, Prompt: request.Prompt, WorkDir: request.Project.RepositoryPath, Model: request.Project.Model, OutputSchema: request.OutputSchema, WorkspaceWrite: request.WorkspaceWrite, Ephemeral: request.Isolated}
	turnCtx, cancelTurn := context.WithTimeout(ctx, runtimePolicy.TurnTimeout)
	defer cancelTurn()
	capabilities := p.Capabilities()
	if capabilities.NativeGoal {
		goal, goalErr := o.store.CurrentGoal(ctx, request.Project.ID)
		if goalErr != nil {
			_ = o.store.FinishRun(ctx, request.RunID, "FAILED", "FAILED")
			return result, fmt.Errorf("load native provider goal: %w", goalErr)
		}
		runRequest.GoalObjective = goal.Title + "\n\n" + goal.Objective
		if capabilities.NativeTokenBudget {
			if budget, budgetErr := o.store.ProjectBudgetUsage(ctx, request.Project.ID); budgetErr == nil && budget.TokenLimit > 0 {
				value := budget.TokenLimit
				runRequest.GoalTokenBudget = &value
			} else if budgetErr != nil && !errors.Is(budgetErr, store.ErrNotFound) {
				_ = o.store.FinishRun(ctx, request.RunID, "FAILED", "FAILED")
				return result, fmt.Errorf("load native provider token budget: %w", budgetErr)
			}
		}
	}
	var events <-chan provider.Event
	var err error
	if request.Isolated {
		events, err = p.Start(turnCtx, runRequest)
	} else if session, sessionErr := o.store.ActiveSession(ctx, request.Project.ID, p.Name()); sessionErr == nil {
		result.Resumed = true
		result.SessionID = session.SessionID
		events, err = p.Resume(turnCtx, session.SessionID, runRequest)
		if errors.Is(err, provider.ErrSessionInvalid) {
			invalidateErr := o.store.InvalidateSession(ctx, request.Project.ID, p.Name(), session.SessionID, "provider rejected session resume", 30*24*time.Hour)
			if invalidateErr != nil {
				err = errors.Join(err, invalidateErr)
			} else {
				result.Resumed = false
				result.SessionID = ""
				events, err = p.Start(turnCtx, runRequest)
			}
		}
	} else if errors.Is(sessionErr, store.ErrNotFound) {
		events, err = p.Start(turnCtx, runRequest)
	} else {
		err = sessionErr
	}
	if err != nil {
		_ = o.store.FinishRun(ctx, request.RunID, "FAILED", "FAILED")
		return result, err
	}
	completed := false
	var runErr error
	var control *store.ControlRequest
	interrupted := false
	ticker := time.NewTicker(o.controlPoll)
	defer ticker.Stop()
	streamOpen := true
	for streamOpen {
		select {
		case <-turnCtx.Done():
			timeoutErr := ErrTurnTimeout
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				timeoutErr = ErrRunTimeout
			}
			interruptErr := p.Interrupt(context.WithoutCancel(ctx), request.RunID)
			runErr = errors.Join(timeoutErr, turnCtx.Err())
			if interruptErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("interrupt timed-out provider: %w", interruptErr))
			}
			streamOpen = false
		case event, ok := <-events:
			if !ok {
				streamOpen = false
				continue
			}
			event.RunID = request.RunID
			if len(event.Raw) == 0 {
				event.Raw = marshalSynthetic(event)
			}
			if event.SessionID != "" {
				result.SessionID = event.SessionID
			}
			if event.Message != "" {
				result.FinalMessage = event.Message
			}
			var recordErr error
			if request.Isolated {
				recordErr = o.store.RecordEphemeralProviderEvent(ctx, request.Project.ID, event)
			} else {
				recordErr = o.store.RecordProviderEvent(ctx, request.Project.ID, event)
			}
			if recordErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("record provider event: %w", recordErr))
			}
			if event.Type == provider.EventCompleted {
				completed = true
			}
			if event.Type == provider.EventFailed || event.Err != nil {
				if control != nil && interrupted {
					continue
				}
				if event.Err != nil {
					runErr = errors.Join(runErr, event.Err)
				} else {
					runErr = errors.Join(runErr, errors.New(event.Message))
				}
			}
		case <-ticker.C:
			if control == nil {
				pending, controlErr := o.store.PendingRunControl(ctx, request.RunID)
				if controlErr == nil {
					control = &pending
				} else if !errors.Is(controlErr, store.ErrNotFound) {
					runErr = errors.Join(runErr, controlErr)
				}
			}
			if control != nil && !interrupted && (control.Action == "CANCEL" || time.Since(control.RequestedAt) >= o.drainGrace) {
				if interruptErr := p.Interrupt(ctx, request.RunID); interruptErr != nil {
					runErr = errors.Join(runErr, fmt.Errorf("interrupt provider: %w", interruptErr))
				} else {
					interrupted = true
				}
			}
		}
	}
	if control == nil {
		if pending, controlErr := o.store.PendingRunControl(ctx, request.RunID); controlErr == nil {
			control = &pending
		} else if !errors.Is(controlErr, store.ErrNotFound) {
			runErr = errors.Join(runErr, controlErr)
		}
	}
	if control != nil {
		return o.finishControlledRun(ctx, request, result, *control, runErr)
	}
	if !completed {
		runErr = errors.Join(runErr, errors.New("provider stream ended without a completion event"))
	}
	if runErr != nil {
		if handled, quotaResult, quotaErr := o.handleRuntimeQuotaFailure(ctx, p, request, result); handled {
			return quotaResult, quotaErr
		}
		if err := o.store.FinishRun(context.WithoutCancel(ctx), request.RunID, "FAILED", "FAILED"); err != nil {
			runErr = errors.Join(runErr, err)
		}
		return result, runErr
	}
	if request.ReadOnlyTask {
		if err := o.store.FinishRun(ctx, request.RunID, "COMPLETED", "READY"); err != nil {
			return result, err
		}
		result.State = "COMPLETED"
		result.Usage, err = o.store.RunUsage(ctx, request.RunID)
		if err == nil && handoff != nil {
			err = o.store.ConsumeHandoff(ctx, handoff.ID, request.RunID)
		}
		return result, err
	}
	if err := o.store.FinishRun(ctx, request.RunID, "VERIFYING", "VERIFYING"); err != nil {
		return result, err
	}
	result.State = "VERIFYING"
	result.Usage, err = o.store.RunUsage(ctx, request.RunID)
	if err == nil && handoff != nil {
		err = o.store.ConsumeHandoff(ctx, handoff.ID, request.RunID)
	}
	return result, err
}

func (o *Orchestrator) finishControlledRun(ctx context.Context, request Request, result Result, control store.ControlRequest, runErr error) (Result, error) {
	result.State = "FAILED"
	if runErr != nil {
		_ = o.store.CompleteRunControl(ctx, control.ID, "FAILED")
		_ = o.store.FinishRun(ctx, request.RunID, "FAILED", "FAILED")
		return result, runErr
	}
	if err := o.store.FinishRun(ctx, request.RunID, "DRAINING", "CHECKPOINTING"); err != nil {
		_ = o.store.CompleteRunControl(ctx, control.ID, "FAILED")
		return result, err
	}
	snapshot, err := o.inspector.Snapshot(ctx, request.Project.RepositoryPath)
	if err != nil {
		_ = o.store.CompleteRunControl(ctx, control.ID, "FAILED")
		return result, err
	}
	goal, err := o.store.CurrentGoal(ctx, request.Project.ID)
	if err != nil {
		_ = o.store.CompleteRunControl(ctx, control.ID, "FAILED")
		return result, err
	}
	nextAction := "resume the interrupted work item from its preserved repository state"
	projectState := "BLOCKED"
	if control.Action == "CANCEL" {
		nextAction = "review the cancelled execution and decide whether to create a replacement work item"
		projectState = "CANCELLED"
	}
	_, err = o.store.CreateCheckpoint(ctx, store.Checkpoint{ProjectID: request.Project.ID, RunID: request.RunID, GoalVersion: goal.Version, WorkItemID: request.WorkItemID, Provider: request.Project.Provider, Model: request.Project.Model, SessionID: result.SessionID, CommitSHA: snapshot.CommitSHA, Branch: snapshot.Branch, DirtyFiles: snapshot.DirtyFiles, DirtyFingerprint: snapshot.DirtyFingerprint, CompletedSummary: result.FinalMessage, RemainingSteps: "execution stopped by " + control.Action, NextAction: nextAction})
	if err != nil {
		_ = o.store.CompleteRunControl(ctx, control.ID, "FAILED")
		return result, err
	}
	if err = o.store.TransitionProjectState(ctx, request.Project.ID, "CHECKPOINTING", projectState); err != nil {
		_ = o.store.CompleteRunControl(ctx, control.ID, "FAILED")
		return result, err
	}
	if err = o.store.CompleteRunControl(ctx, control.ID, "HANDLED"); err != nil {
		return result, err
	}
	result.State = projectState
	result.Usage, err = o.store.RunUsage(ctx, request.RunID)
	return result, err
}

func marshalSynthetic(event provider.Event) json.RawMessage {
	payload := map[string]any{"type": event.Type, "message": event.Message}
	if event.Err != nil {
		payload["error"] = event.Err.Error()
	}
	raw, _ := json.Marshal(payload)
	return raw
}
