package observer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// SchedulePolicy decides when a supply pass is worth running.
type SchedulePolicy struct {
	// Interval is how often a project is looked at even when nothing has
	// happened. A repository can drift away from its criteria without a commit
	// — a pack revision, an exception expiring — so "nothing changed" is not
	// the same as "nothing to check".
	Interval time.Duration
	// BacklogFloor triggers a pass when fewer than this many runnable items
	// are waiting. It is the difference between a supplier that keeps the
	// board fed and one that fills it.
	BacklogFloor int
	// DiscoveryBudget caps how many passes may run in a day.
	//
	// It is separate from the implementation budget on purpose. Looking for
	// work and doing work compete for the same tokens, and a discovery loop
	// that can borrow from implementation will spend the day deciding what to
	// do and none of it doing anything.
	DiscoveryBudget int
}

// DefaultSchedulePolicy is the specification's recommended starting point.
func DefaultSchedulePolicy() SchedulePolicy {
	return SchedulePolicy{Interval: 24 * time.Hour, BacklogFloor: 3, DiscoveryBudget: 4}
}

// Decision is whether to run a pass, and why or why not.
type Decision struct {
	Trigger string
	Reason  string
}

// Run reports whether a pass should happen.
func (d Decision) Run() bool { return d.Trigger != "" }

// ErrDiscoveryBudgetSpent means no more passes may run until the next window.
var ErrDiscoveryBudgetSpent = errors.New("discovery budget spent for today")

// Due decides whether to run a supply pass now.
//
// The order matters. The budget is checked first, so an exhausted budget is
// reported as an exhausted budget rather than as "nothing to do" — the two look
// identical from outside and call for completely different responses.
func Due(ctx context.Context, db *store.Store, projectID, goalID, headSHA string, policy SchedulePolicy, now time.Time) (Decision, error) {
	if policy.Interval <= 0 {
		policy = DefaultSchedulePolicy()
	}
	if policy.DiscoveryBudget > 0 {
		spent, err := db.SupplyRunsSince(ctx, projectID, now.Add(-24*time.Hour))
		if err != nil {
			return Decision{}, err
		}
		if spent >= policy.DiscoveryBudget {
			return Decision{}, fmt.Errorf("%w: 오늘 %d회", ErrDiscoveryBudgetSpent, spent)
		}
	}
	last, err := db.LastCompletedSupplyRun(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		return Decision{Trigger: store.TriggerScheduled, Reason: "이 프로젝트를 아직 한 번도 평가하지 않았습니다"}, nil
	}
	if err != nil {
		return Decision{}, err
	}
	if last.CommitSHA != headSHA {
		return Decision{Trigger: store.TriggerCommitChanged,
			Reason: fmt.Sprintf("기본 브랜치가 %s 에서 %s 로 바뀌었습니다", short(last.CommitSHA), short(headSHA))}, nil
	}
	// The commit is the same one the last pass read. Reading it again produces
	// the same answers and spends budget doing it, so only a reason unrelated
	// to the code justifies another look.
	runnable, err := db.RunnableWorkCount(ctx, goalID)
	if err != nil {
		return Decision{}, err
	}
	if policy.BacklogFloor > 0 && runnable < policy.BacklogFloor {
		return Decision{Trigger: store.TriggerBacklogLow,
			Reason: fmt.Sprintf("실행 가능한 대기 작업이 %d건입니다", runnable)}, nil
	}
	if now.Sub(last.EndedAt) >= policy.Interval {
		return Decision{Trigger: store.TriggerScheduled,
			Reason: fmt.Sprintf("마지막 평가로부터 %s 지났습니다", now.Sub(last.EndedAt).Round(time.Hour))}, nil
	}
	return Decision{Reason: fmt.Sprintf("마지막 평가 이후 %s 에서 바뀐 것이 없습니다", short(headSHA))}, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// PassResult is what one scheduled attempt did.
type PassResult struct {
	Decision Decision
	// Ran is false when the decision was not to run, or when another process
	// was already running the same pass.
	Ran    bool
	Result SupplyResult
	Detail string
}

// RunScheduledPass decides whether a pass is due and, if so, runs it exactly
// once.
//
// Three things have to hold at once and each is easy to lose on its own: a
// pass runs only when there is a reason, the same event maps to one pass
// however many times it is delivered, and a pass that crashes does not wedge
// the project.
func RunScheduledPass(ctx context.Context, db *store.Store, req PassRequest) (PassResult, error) {
	decision, err := Due(ctx, db, req.ProjectID, req.GoalID, req.HeadSHA, req.Schedule, time.Now())
	if err != nil {
		return PassResult{}, err
	}
	if !decision.Run() {
		return PassResult{Decision: decision, Detail: decision.Reason}, nil
	}
	run := store.SupplyRun{ProjectID: req.ProjectID, CommitSHA: req.HeadSHA, Trigger: decision.Trigger}
	_, started, err := db.BeginSupplyRun(ctx, run)
	if err != nil {
		return PassResult{Decision: decision}, err
	}
	if !started {
		// Somebody else has this event, or already finished it. Saying so is
		// the difference between "nothing to do" and "somebody else is doing
		// it", and an operator watching two machines needs to tell them apart.
		return PassResult{Decision: decision, Detail: "같은 이벤트를 이미 처리했거나 처리 중입니다"}, nil
	}
	result, err := ObserveAndSupply(ctx, db, req.ProjectID, req.GoalID, req.Repository, req.HeadSHA,
		req.ToolVersion, req.Pack, req.Profile, req.Detectors, req.Supply)
	if err != nil {
		// The pass is closed as failed rather than left running, so the next
		// attempt sees a finished attempt and retries instead of waiting out
		// the abandonment timeout.
		if finishErr := db.FinishSupplyRun(ctx, run.Key(), store.SupplyRunFailed, err.Error(), store.SupplyCounts{}); finishErr != nil {
			return PassResult{Decision: decision, Ran: true}, errors.Join(err, finishErr)
		}
		return PassResult{Decision: decision, Ran: true}, err
	}
	counts := store.SupplyCounts{Filed: len(result.Filed), AlreadyFiled: len(result.AlreadyFiled),
		Deferred: len(result.Deferred), Unchecked: len(result.Unchecked)}
	if err = db.FinishSupplyRun(ctx, run.Key(), store.SupplyRunCompleted, decision.Reason, counts); err != nil {
		return PassResult{Decision: decision, Ran: true, Result: result}, err
	}
	return PassResult{Decision: decision, Ran: true, Result: result, Detail: decision.Reason}, nil
}

// PassRequest is everything one scheduled pass needs.
type PassRequest struct {
	ProjectID, GoalID, Repository, HeadSHA, ToolVersion string
	Pack                                                standards.Pack
	Profile                                             standards.Profile
	Detectors                                           []Detector
	Schedule                                            SchedulePolicy
	Supply                                              SupplyPolicy
}
