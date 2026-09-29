package sqlite

import (
	"context"
)

// Delivery states, from "the work itself is verified" to "the product is
// releasable".
//
// They exist because DONE answers one question only: did this work item's own
// verification pass. Whether the change reached the default branch, whether
// the branches held up together, and whether anyone may ship it are three
// further questions with three further answers. A board that folds them into
// the DONE column tells a reader that finished work is released work, which is
// how a broken default branch sits behind a wall of green cards.
const (
	DeliveryVerified           = "VERIFIED"
	DeliveryAwaitingApproval   = "AWAITING_APPROVAL"
	DeliveryRejected           = "REJECTED"
	DeliveryMerging            = "MERGING"
	DeliveryMergeFailed        = "MERGE_FAILED"
	DeliveryIntegrationPending = "INTEGRATION_PENDING"
	DeliveryIntegrationFailed  = "INTEGRATION_FAILED"
	DeliveryReleasable         = "RELEASABLE"
)

// DeliveryBadge is how far a finished work item got toward being released.
type DeliveryBadge struct {
	State  string `json:"state"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
	// Blocking is true when this badge is a reason not to ship, so a board can
	// colour it without repeating the state list.
	Blocking bool `json:"blocking"`
}

// deliveryLabels is the one place a state becomes words, so the board, the CLI
// and any later surface say the same thing about the same state.
var deliveryLabels = map[string]string{
	DeliveryVerified:           "검증 완료",
	DeliveryAwaitingApproval:   "병합 승인 대기",
	DeliveryRejected:           "병합 반려",
	DeliveryMerging:            "병합 진행 중",
	DeliveryMergeFailed:        "병합 실패",
	DeliveryIntegrationPending: "병합됨 · 통합 검증 대기",
	DeliveryIntegrationFailed:  "병합됨 · 통합 검증 실패",
	DeliveryReleasable:         "출시 가능",
}

var deliveryBlocking = map[string]bool{
	DeliveryRejected: true, DeliveryMergeFailed: true, DeliveryIntegrationFailed: true,
}

// DeliveryLabel is the human name of a delivery state.
func DeliveryLabel(state string) string {
	if label, ok := deliveryLabels[state]; ok {
		return label
	}
	return state
}

func deliveryBadge(state, detail string) DeliveryBadge {
	return DeliveryBadge{State: state, Label: DeliveryLabel(state), Detail: detail, Blocking: deliveryBlocking[state]}
}

// deliveryBadges computes one badge per work item in a goal.
//
// It runs three queries for the whole goal rather than three per card: a board
// showing two hundred items would otherwise open six hundred, and the screen
// that is supposed to make the state legible would be the slowest one.
func (s *Store) deliveryBadges(ctx context.Context, projectID, goalID string) (map[string]DeliveryBadge, error) {
	badges := map[string]DeliveryBadge{}
	integration, err := s.IntegrationStatus(ctx, projectID)
	if err != nil {
		return nil, err
	}
	approvals, err := s.lastByWorkItem(ctx, `SELECT a.work_item_id,a.status FROM approvals a
JOIN work_items w ON w.id=a.work_item_id
WHERE w.goal_id=? AND a.action_type=? ORDER BY a.requested_at`, goalID, ApprovalMergeBranch)
	if err != nil {
		return nil, err
	}
	effects, err := s.lastByWorkItem(ctx, `SELECT e.work_item_id,e.state FROM external_effects e
JOIN work_items w ON w.id=e.work_item_id
WHERE w.goal_id=? AND e.kind=? ORDER BY e.updated_at`, goalID, EffectMergeBranch)
	if err != nil {
		return nil, err
	}
	for id, state := range effects {
		badges[id] = mergeEffectBadge(state, integration)
	}
	for id, status := range approvals {
		if _, decided := badges[id]; decided {
			// A merge was attempted, so the approval is already spent and the
			// attempt's outcome is the newer fact.
			continue
		}
		switch status {
		case "PENDING":
			badges[id] = deliveryBadge(DeliveryAwaitingApproval, "사람의 병합 승인을 기다립니다")
		case "REJECTED":
			badges[id] = deliveryBadge(DeliveryRejected, "병합이 반려되었습니다")
		}
	}
	return badges, nil
}

func mergeEffectBadge(state string, integration IntegrationCheck) DeliveryBadge {
	switch state {
	case EffectSucceeded:
		switch {
		// A failed integration check leaves the branch pending too — the flag
		// means "not known good", which covers both "never run" and "run and
		// failed". Asking about the pending flag first would report a failure
		// as an absence, and an operator reading "아직 검증되지 않았습니다"
		// waits for a check that already came back broken.
		case integration.LastSHA != "" && !integration.LastPassed:
			return deliveryBadge(DeliveryIntegrationFailed, integrationDetail(integration,
				"병합된 결과의 통합 검증이 실패했습니다"))
		case integration.Pending:
			return deliveryBadge(DeliveryIntegrationPending, integrationDetail(integration,
				"병합되었지만 합쳐진 결과는 아직 검증되지 않았습니다"))
		case integration.LastPassed:
			return deliveryBadge(DeliveryReleasable, "병합되었고 통합 검증도 통과했습니다")
		default:
			// Merged into a project that has never run an integration check.
			// Saying "releasable" here would be asserting a check nobody ran.
			return deliveryBadge(DeliveryIntegrationPending, "병합되었지만 통합 검증 기록이 없습니다")
		}
	case EffectFailed:
		return deliveryBadge(DeliveryMergeFailed, "병합을 시도했으나 실패했습니다")
	case EffectIntended, EffectUnknown:
		return deliveryBadge(DeliveryMerging, "병합 결과가 아직 확정되지 않았습니다")
	}
	return deliveryBadge(DeliveryVerified, "")
}

func integrationDetail(integration IntegrationCheck, fallback string) string {
	if integration.Reason != "" {
		return integration.Reason
	}
	return fallback
}

// lastByWorkItem keeps the newest value per work item from a query ordered
// oldest-first.
func (s *Store) lastByWorkItem(ctx context.Context, query string, args ...any) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var id, value string
		if err = rows.Scan(&id, &value); err != nil {
			return nil, err
		}
		if id == "" {
			continue
		}
		values[id] = value
	}
	return values, rows.Err()
}
