package sqlite

import (
	"context"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/model"
)

// BoardCard is one work item as the board shows it.
//
// Everything a person needs to decide "can this run, and if not why" is on the
// card. A board that shows a title and a status makes the reader open each one
// to find out it was blocked.
type BoardCard struct {
	Item model.WorkItem `json:"item"`
	// Blockers are the engine's own reasons this cannot proceed, taken from
	// the same place the runner takes them. A board that computes its own
	// version of "why is this waiting" will eventually disagree with the thing
	// that actually decides.
	Blockers []WorkItemBlocker `json:"blockers"`
	// Verification is the latest gate outcome for this item's runs, so a card
	// says whether what it produced held up.
	Verification string `json:"verification"`
	// Dependencies is how many predecessors it declares, and Dependents how
	// many wait on it — the second is what says whether finishing it unblocks
	// anything.
	Dependencies int `json:"dependencies"`
	Dependents   int `json:"dependents"`
	// AllowedTargets and RefusedTargets are the columns a person may and may
	// not drag this into, with the reason for each refusal. They come from the
	// server so the board cannot offer a move the server will reject.
	AllowedTargets []string          `json:"allowed_targets"`
	RefusedTargets map[string]string `json:"refused_targets"`
	// Delivery is how far a finished item got toward release. It is separate
	// from the column on purpose: DONE means this item's own verification
	// passed, and merge, integration and release are later facts that a column
	// cannot carry without claiming one when only the other is true.
	Delivery DeliveryBadge `json:"delivery"`
}

// Runnable reports whether nothing stands in this item's way.
func (c BoardCard) Runnable() bool { return len(c.Blockers) == 0 }

// BoardColumn is one status with its cards and its true total.
//
// Total is separate from the cards because a column shows a page of them: a
// count taken from the rendered list would say "12" on a board holding 400.
type BoardColumn struct {
	Status string      `json:"status"`
	Label  string      `json:"label"`
	Cards  []BoardCard `json:"cards"`
	Total  int         `json:"total"`
	// Manual says whether a person may drop a card here at all.
	Manual bool `json:"manual"`
}

// Board is the whole control screen's data.
type Board struct {
	GoalID    string        `json:"goal_id"`
	GoalTitle string        `json:"goal_title"`
	Columns   []BoardColumn `json:"columns"`
	// InProgress and WIPLimit say how much may run at once and how much is,
	// which is the other half of "why is nothing starting".
	InProgress int `json:"in_progress"`
	WIPLimit   int `json:"wip_limit"`
	// Filtered is true when a query narrowed the board, so the totals can be
	// read as "matching" rather than "all".
	Filtered bool `json:"filtered"`
}

// BoardQuery narrows what the board shows.
type BoardQuery struct {
	Search string
	// Statuses limits which columns are populated; empty means all of them.
	Statuses []string
	// FailedVerificationOnly keeps only items whose last run failed a gate,
	// which is the filter someone reaches for when triaging.
	FailedVerificationOnly bool
	// PerColumn caps the cards returned per column. Totals are unaffected.
	PerColumn int
}

const defaultCardsPerColumn = 50

// BoardFor assembles the control screen for a goal.
func (s *Store) BoardFor(ctx context.Context, projectID string, goal model.Goal, query BoardQuery) (Board, error) {
	board := Board{GoalID: goal.ID, GoalTitle: goal.Title}
	board.Filtered = strings.TrimSpace(query.Search) != "" || len(query.Statuses) > 0 || query.FailedVerificationOnly
	limit := query.PerColumn
	if limit <= 0 {
		limit = defaultCardsPerColumn
	}
	items, err := s.SearchWorkItems(ctx, goal.ID, WorkItemQuery{Search: query.Search})
	if err != nil {
		return board, err
	}
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		return board, err
	}
	board.WIPLimit = project.WIPLimit
	dependents, err := s.dependentCounts(ctx, goal.ID)
	if err != nil {
		return board, err
	}
	delivery, err := s.deliveryBadges(ctx, projectID, goal.ID)
	if err != nil {
		return board, err
	}
	wanted := map[string]bool{}
	for _, status := range query.Statuses {
		wanted[strings.ToUpper(strings.TrimSpace(status))] = true
	}
	grouped := map[string][]BoardCard{}
	for _, item := range items {
		if item.Status == "IN_PROGRESS" {
			board.InProgress++
		}
		if len(wanted) > 0 && !wanted[item.Status] {
			continue
		}
		card, cardErr := s.boardCard(ctx, projectID, goal.ID, item, dependents)
		if cardErr != nil {
			return board, cardErr
		}
		if badge, ok := delivery[item.ID]; ok {
			card.Delivery = badge
		} else if item.Status == "DONE" {
			card.Delivery = deliveryBadge(DeliveryVerified, "검증은 끝났고 아직 병합되지 않았습니다")
		}
		if query.FailedVerificationOnly && card.Verification != "FAILED" {
			continue
		}
		grouped[item.Status] = append(grouped[item.Status], card)
	}
	for _, status := range BoardStatuses {
		cards := grouped[status]
		// Highest priority first: the board's default order is the order the
		// planner would pick, so the top of a column is the next candidate.
		sort.SliceStable(cards, func(i, j int) bool { return cards[i].Item.Priority > cards[j].Item.Priority })
		column := BoardColumn{Status: status, Label: StatusLabel(status), Total: len(cards), Manual: ManualStatus(status)}
		if len(cards) > limit {
			cards = cards[:limit]
		}
		column.Cards = cards
		if column.Cards == nil {
			column.Cards = []BoardCard{}
		}
		board.Columns = append(board.Columns, column)
	}
	return board, nil
}

func (s *Store) boardCard(ctx context.Context, projectID, goalID string, item model.WorkItem, dependents map[string]int) (BoardCard, error) {
	card := BoardCard{Item: item, Blockers: []WorkItemBlocker{},
		AllowedTargets: AllowedManualTargets(item.Status), RefusedTargets: RefusedManualTargets(item.Status),
		Dependents: dependents[item.ID]}
	detail, err := s.WorkItemDetails(ctx, projectID, goalID, item.ID)
	if err != nil {
		if err == ErrNotFound {
			return card, nil
		}
		return card, err
	}
	if detail.Blockers != nil {
		card.Blockers = detail.Blockers
	}
	card.Dependencies = len(detail.Dependencies)
	for _, run := range detail.Runs {
		results, resultErr := s.VerificationsForRun(ctx, run.ID)
		if resultErr != nil {
			return card, resultErr
		}
		for _, result := range results {
			if result.Required && result.Status != "PASSED" {
				card.Verification = "FAILED"
			} else if card.Verification == "" && result.Required {
				card.Verification = "PASSED"
			}
		}
		if card.Verification != "" {
			break
		}
	}
	return card, nil
}

// dependentCounts is how many items wait on each one, which is what says
// whether finishing something unblocks anything else.
func (s *Store) dependentCounts(ctx context.Context, goalID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.depends_on_id,COUNT(*) FROM work_item_dependencies d
JOIN work_items w ON w.id=d.work_item_id WHERE w.goal_id=? GROUP BY d.depends_on_id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var count int
		if err = rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		counts[id] = count
	}
	return counts, rows.Err()
}

// SeedBoardItem inserts a work item directly. It exists so a test can compose
// a board in a particular shape without driving the planner to produce one.
func (s *Store) SeedBoardItem(ctx context.Context, goalID, id, title, status string, priority float64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO work_items(id,goal_id,type,title,status,weight,priority,version) VALUES(?,?,'IMPLEMENT',?,?,1,?,1)`,
		id, goalID, title, status, priority)
	return err
}
