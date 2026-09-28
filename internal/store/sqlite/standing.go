package sqlite

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/policy"
)

// Standings say how far a recorded statement can be trusted as current.
//
// The distinction matters because a session is handed all of this as context
// and, unmarked, reads a two-month-old architecture note with the same
// authority as a gate that passed a minute ago. A decision is true by fiat when
// it is made; whether it is still true depends on whether the code it was
// about has moved since.
const (
	// StandingCurrent means the code this was reasoning about has not changed
	// since it was recorded.
	StandingCurrent = "CURRENT"
	// StandingReviewNeeded means the code under this decision's scope has
	// changed since it was made. The decision is not wrong — nobody has
	// checked, which is exactly the point.
	StandingReviewNeeded = "REVIEW_NEEDED"
	// StandingUnanchored means nothing recorded which code the decision was
	// about, so staleness cannot be judged at all.
	StandingUnanchored = "UNANCHORED"
)

// DecisionStanding is one decision with what is known about its currency.
type DecisionStanding struct {
	Decision DesignDecision
	Standing string
	// ChangedFiles are the files under the decision's scope that moved since
	// it was recorded, capped for display. Naming them is what turns "this
	// might be stale" into something a person can act on.
	ChangedFiles []string
	// TotalChanged is how many changed in total, which may exceed the names.
	TotalChanged int
	// Detail explains the standing in the user's terms.
	Detail string
}

const changedFileSample = 5

// DecisionStandings judges decisions against the repository as it stands now.
//
// A decision that pinned no commit cannot be judged, and one whose commit is
// no longer in the repository cannot either; both are reported as such rather
// than being assumed current. Assuming current is how a rewritten module keeps
// being described to every new session by the note that preceded it.
func DecisionStandings(ctx context.Context, repositoryPath string, decisions []DesignDecision) []DecisionStanding {
	// The diff for one baseline is reused across every decision that shares
	// it, because a project usually records several decisions per commit and
	// shelling out per decision would make assembling context slow enough to
	// be skipped.
	cache := map[string][]string{}
	result := make([]DecisionStanding, 0, len(decisions))
	for _, decision := range decisions {
		result = append(result, judgeDecision(ctx, repositoryPath, decision, cache))
	}
	return result
}

func judgeDecision(ctx context.Context, repositoryPath string, decision DesignDecision, cache map[string][]string) DecisionStanding {
	standing := DecisionStanding{Decision: decision}
	if strings.TrimSpace(decision.BaseCommit) == "" {
		standing.Standing = StandingUnanchored
		standing.Detail = "이 결정이 어떤 코드를 두고 내려졌는지 기록되지 않아 현재도 유효한지 판단할 수 없습니다"
		return standing
	}
	changed, ok := cache[decision.BaseCommit]
	if !ok {
		files, err := gitops.ChangedSince(ctx, repositoryPath, decision.BaseCommit)
		if err != nil {
			if errors.Is(err, gitops.ErrUnknownCommit) {
				standing.Standing = StandingUnanchored
				standing.Detail = "기록된 기준 커밋 " + shortSHA(decision.BaseCommit) + " 을(를) 저장소에서 찾을 수 없어 판단할 수 없습니다"
				return standing
			}
			standing.Standing = StandingUnanchored
			standing.Detail = "저장소를 읽을 수 없어 판단할 수 없습니다: " + err.Error()
			return standing
		}
		changed = files
		cache[decision.BaseCommit] = files
	}
	// A decision without a scope is about the project as a whole, so any
	// change is relevant to it. A decision with one is only unsettled by
	// changes to the files it named.
	var relevant []string
	for _, file := range changed {
		if decision.Scope == "" || policy.PathInScope(decision.Scope, file) {
			relevant = append(relevant, file)
		}
	}
	if len(relevant) == 0 {
		standing.Standing = StandingCurrent
		standing.Detail = "기준 커밋 이후 이 결정이 다루는 코드는 바뀌지 않았습니다"
		return standing
	}
	sort.Strings(relevant)
	standing.Standing = StandingReviewNeeded
	standing.TotalChanged = len(relevant)
	if len(relevant) > changedFileSample {
		standing.ChangedFiles = relevant[:changedFileSample]
	} else {
		standing.ChangedFiles = relevant
	}
	standing.Detail = fmt.Sprintf("기준 커밋 %s 이후 %d개 파일이 바뀌었습니다: %s",
		shortSHA(decision.BaseCommit), len(relevant), strings.Join(standing.ChangedFiles, ", "))
	if standing.TotalChanged > len(standing.ChangedFiles) {
		standing.Detail += fmt.Sprintf(" 외 %d개", standing.TotalChanged-len(standing.ChangedFiles))
	}
	return standing
}

// NeedsReview reports whether this decision should be re-confirmed before
// being relied on.
func (d DecisionStanding) NeedsReview() bool { return d.Standing != StandingCurrent }
