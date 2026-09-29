package observer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// AutonomyPolicy is the envelope inside which automation may approve its own
// findings for execution.
//
// It approves work to *run*, and nothing further. The merge boundary stays a
// person's: a supplier that could approve its own merges would be the only
// reviewer of its own work, and the review would be a formality performed by
// the thing being reviewed.
type AutonomyPolicy struct {
	// Enabled is false by default. A default that runs is a default nobody
	// chose.
	Enabled bool
	// AllowedStandards names the criteria whose findings may be approved
	// automatically. Empty means none — an empty allowlist is an allowlist.
	AllowedStandards []string
	// AllStandards opens the envelope to every criterion. An operator who
	// wants the whole loop running should not have to enumerate forty IDs, and
	// enumerating them badly is worse than saying plainly that all are in.
	AllStandards bool
	// AllowedScopes limits where the work may reach.
	AllowedScopes []string
	// AllScopes removes the scope limit.
	AllScopes bool
	// MaxTokens caps the size of a single piece of work.
	MaxTokens int64
	// DailyLimit caps how many approvals may be made in a day.
	DailyLimit int
	// AutoMerge grants the merge approval too, once the work is finished and
	// its gates have passed on the commit being released.
	//
	// It is a separate switch because it is a separate decision: approving
	// execution keeps the change in its own worktree, and approving a merge
	// puts it on the branch everyone else builds from.
	AutoMerge bool
}

// AutoDecisions is what one automatic approval pass did.
type AutoDecisions struct {
	Approved []string
	// Refused maps a work item to why the envelope did not admit it. Work the
	// mechanism has no business in — anything a person wrote — is absent from
	// both lists rather than reported as refused.
	Refused map[string]string
	Detail  string
}

// AutoApprove admits the findings the envelope allows.
func AutoApprove(ctx context.Context, db *store.Store, projectID, goalID string,
	policy AutonomyPolicy) (AutoDecisions, error) {
	decisions := AutoDecisions{Refused: map[string]string{}}
	if !policy.Enabled {
		decisions.Detail = "자동 승인이 켜져 있지 않습니다"
		return decisions, nil
	}
	now := time.Now().UTC()
	spent, err := db.AutoApprovalsSince(ctx, projectID, now.Add(-24*time.Hour))
	if err != nil {
		return decisions, err
	}
	remaining := policy.DailyLimit - spent
	if policy.DailyLimit > 0 && remaining <= 0 {
		decisions.Detail = fmt.Sprintf("오늘의 자동 승인 한도를 다 썼습니다 (%d건)", spent)
		return decisions, nil
	}
	findings, err := db.SuppliedFindings(ctx, projectID)
	if err != nil {
		return decisions, err
	}
	claims, err := db.GateClaims(ctx, projectID)
	if err != nil {
		return decisions, err
	}
	judgedBy := map[string][]string{}
	for checkType, claim := range claims {
		for _, standardID := range claim.Settles {
			judgedBy[standardID] = append(judgedBy[standardID], checkType)
		}
	}
	items, err := db.ListWorkItems(ctx, goalID)
	if err != nil {
		return decisions, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Priority > items[j].Priority })
	for _, item := range items {
		if item.Status != "BACKLOG" {
			continue
		}
		standardID, supplied := findings[item.ID]
		if !supplied {
			// Work a person wrote is not the supplier's to approve. Automation
			// given a standing approval for its own findings must not inherit
			// one for everything on the board.
			continue
		}
		gates := judgedBy[standardID]
		if reason := admits(policy, item, standardID, gates); reason != "" {
			decisions.Refused[item.ID] = reason
			continue
		}
		previous, err := db.AutoApprovalFor(ctx, item.ID)
		switch {
		case err == nil && previous.Settled && !previous.Passed:
			// Whatever stopped it is still there, and a second identical
			// attempt spends budget to reach the same place.
			decisions.Refused[item.ID] = "이전 자동 시도가 실패했습니다: " + previous.Outcome
			continue
		case err != nil && err != store.ErrNotFound:
			return decisions, err
		}
		if policy.DailyLimit > 0 && len(decisions.Approved) >= remaining {
			decisions.Detail = fmt.Sprintf("오늘의 자동 승인 한도에 도달했습니다 (%d건)", policy.DailyLimit)
			break
		}
		basis := fmt.Sprintf("기준 %s · 범위 %s · 판정 게이트 %s", standardID, item.ChangeScope, strings.Join(gates, ", "))
		if _, err = db.ApplyAutomaticTransition(ctx, goalID, item.ID, "APPROVED", item.Version); err != nil {
			return decisions, err
		}
		if err = db.RecordAutoApproval(ctx, store.AutoApprovalRecord{WorkItemID: item.ID, ProjectID: projectID,
			StandardID: standardID, Basis: basis, ApprovedAt: now}); err != nil {
			return decisions, err
		}
		decisions.Approved = append(decisions.Approved, item.ID)
	}
	return decisions, nil
}

// admits reports why the envelope does not take an item, or "" when it does.
//
// Each dimension is checked separately so the refusal names the one that
// stopped it. "정책에 맞지 않습니다" tells an operator to go and read the
// policy and work out which part.
func admits(policy AutonomyPolicy, item model.WorkItem, standardID string, gates []string) string {
	if len(gates) == 0 {
		// The one that matters most. Approving work nothing can judge means
		// the machine writes code and no gate can say whether it worked, so
		// the change lands as done on the strength of having been attempted.
		return fmt.Sprintf("%s 를 정산할 게이트가 없어 결과를 판정할 수 없습니다", standardID)
	}
	if !policy.AllStandards && !contains(policy.AllowedStandards, standardID) {
		return fmt.Sprintf("기준 %s 는 자동 승인 허용 목록에 없습니다", standardID)
	}
	if !policy.AllScopes && !scopeAllowed(policy.AllowedScopes, item.ChangeScope) {
		return fmt.Sprintf("범위 %s 는 자동 승인 허용 범위 밖입니다", dashIfEmpty(item.ChangeScope))
	}
	if policy.MaxTokens > 0 && item.EstimatedTokens > policy.MaxTokens {
		return fmt.Sprintf("크기가 한도를 넘습니다 (%d > %d 토큰)", item.EstimatedTokens, policy.MaxTokens)
	}
	if strings.EqualFold(item.Risk, "high") {
		// The envelope says where automation may act, not that everything
		// inside it is harmless.
		return "위험도가 높은 작업은 사람이 봅니다"
	}
	return ""
}

func contains(list []string, value string) bool {
	for _, entry := range list {
		if strings.EqualFold(strings.TrimSpace(entry), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

// scopeAllowed reports whether a work item's scope sits inside one the policy
// permits. An item with no scope is refused: unscoped work can reach anything,
// which is the opposite of what an envelope is for.
func scopeAllowed(allowed []string, scope string) bool {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return false
	}
	for _, permitted := range allowed {
		permitted = strings.TrimSpace(permitted)
		if permitted == scope {
			return true
		}
		prefix := strings.TrimSuffix(permitted, "/**")
		if prefix != permitted && (scope == prefix || strings.HasPrefix(scope, prefix+"/")) {
			return true
		}
	}
	return false
}

func dashIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(범위 없음)"
	}
	return value
}
