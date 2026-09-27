package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// Reconciliation is what an unresolved external effect turned out to be.
type Reconciliation struct {
	Effect store.ExternalEffect
	// Resolved is false when the outside world could not be asked, which is
	// its own answer: the effect stays unresolved rather than being guessed at.
	Resolved bool
	Applied  bool
	Detail   string
}

// ReconcileEffect asks the outside world whether an effect actually happened.
// A retry that skips this is how the same PR, push, or merge is made twice:
// the local record going missing says nothing about what the remote did.
func ReconcileEffect(ctx context.Context, db *store.Store, project model.Project, effect store.ExternalEffect) (Reconciliation, error) {
	result := Reconciliation{Effect: effect}
	switch effect.Kind {
	case store.EffectPublishBranch:
		sha, exists, err := gitops.RemoteBranchSHA(ctx, project.RepositoryPath, effect.Target, effect.Branch)
		if err != nil {
			result.Detail = "원격에 확인할 수 없습니다: " + err.Error()
			return result, nil
		}
		result.Resolved = true
		switch {
		case exists && sha == effect.RequestHash:
			result.Applied = true
			result.Detail = fmt.Sprintf("원격 %s 에 이미 %s 로 반영되어 있습니다", effect.Target, shortSHA(sha))
		case exists:
			result.Detail = fmt.Sprintf("원격 %s 의 브랜치가 %s 라 의도한 %s 와 다릅니다", effect.Target, shortSHA(sha), shortSHA(effect.RequestHash))
		default:
			result.Detail = fmt.Sprintf("원격 %s 에 브랜치가 없습니다", effect.Target)
		}
	case store.EffectMergeBranch:
		contained, err := gitops.CommitIsAncestor(ctx, project.RepositoryPath, effect.RequestHash, effect.Target)
		if err != nil {
			result.Detail = "병합 여부를 확인할 수 없습니다: " + err.Error()
			return result, nil
		}
		result.Resolved = true
		result.Applied = contained
		if contained {
			result.Detail = fmt.Sprintf("%s 는 이미 %s 에 포함되어 있습니다", shortSHA(effect.RequestHash), effect.Target)
		} else {
			result.Detail = fmt.Sprintf("%s 는 아직 %s 에 없습니다", shortSHA(effect.RequestHash), effect.Target)
		}
	default:
		result.Detail = "대사 방법이 정의되지 않은 효과입니다: " + effect.Kind
		return result, nil
	}
	if !result.Resolved {
		return result, nil
	}
	state := store.EffectFailed
	if result.Applied {
		state = store.EffectSucceeded
	}
	return result, db.SettleEffect(ctx, effect.Key, state, result.Detail)
}

// ReconcileAll settles everything unresolved for a project. It is run before
// any new external action so a retry cannot repeat something that already
// happened, and it reports what it could not determine rather than assuming.
func ReconcileAll(ctx context.Context, db *store.Store, project model.Project) ([]Reconciliation, error) {
	pending, err := db.UnresolvedEffects(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	results := make([]Reconciliation, 0, len(pending))
	for _, effect := range pending {
		reconciliation, reconcileErr := ReconcileEffect(ctx, db, project, effect)
		if reconcileErr != nil {
			return results, reconcileErr
		}
		results = append(results, reconciliation)
	}
	return results, nil
}

// GuardEffect refuses to start an action whose previous attempt is unresolved
// and could not be settled. Doing it anyway is exactly the duplicate the
// ledger exists to prevent.
func GuardEffect(ctx context.Context, db *store.Store, project model.Project, effect store.ExternalEffect) error {
	existing, created, err := db.BeginEffect(ctx, effect)
	if err != nil {
		return err
	}
	if created {
		return nil
	}
	if existing.State == store.EffectSucceeded {
		return fmt.Errorf("%w: %s", ErrEffectAlreadyApplied, existing.Result)
	}
	if existing.State == store.EffectFailed {
		return db.RetryEffect(ctx, effect.Key)
	}
	reconciliation, err := ReconcileEffect(ctx, db, project, existing)
	if err != nil {
		return err
	}
	switch {
	case reconciliation.Resolved && reconciliation.Applied:
		return fmt.Errorf("%w: %s", ErrEffectAlreadyApplied, reconciliation.Detail)
	case reconciliation.Resolved:
		return db.RetryEffect(ctx, effect.Key)
	default:
		return fmt.Errorf("%w: %s", ErrEffectUnresolved, reconciliation.Detail)
	}
}

// ErrEffectAlreadyApplied means the change is already in place outside, so
// repeating it would duplicate it.
var ErrEffectAlreadyApplied = errors.New("external effect already applied")

// ErrEffectUnresolved means a previous attempt's outcome could not be
// determined, so nothing may be retried until it is.
var ErrEffectUnresolved = errors.New("previous attempt's outcome is unknown")

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
