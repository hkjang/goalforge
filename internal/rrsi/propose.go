package rrsi

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Components is the parts of a GoalForge project's configuration a change may
// touch.
//
// It is a fixed list so "a component nobody has tried" is a question with an
// answer. An open-ended taxonomy makes every proposal novel by inventing a new
// name for what it touched.
var Components = []string{
	"prompt",      // the templates a run is given
	"gate",        // what judges the work
	"model",       // provider and model
	"concurrency", // how much runs at once
	"repair",      // what happens after a failure
	"context",     // what a session is told about earlier work
	"scope",       // how change scopes are drawn
	"budget",      // token, cost and time limits
}

// Structural is the subset whose absence is a capability gap rather than a
// setting. Touching one of these for the first time buys information even when
// the score does not move, which is the only thing novelty is allowed to
// influence.
var Structural = []string{"gate", "context", "repair", "scope"}

// Edit is one change to one component, with the reason it should help.
type Edit struct {
	Component string
	// Hypothesis is what this change is expected to improve and why. It is
	// required because a change with no stated expectation cannot be
	// falsified, and a search that cannot falsify anything revisits the same
	// idea until the budget runs out.
	Hypothesis string
	Detail     string
	// Change is the machine-applicable part, when there is one. Most edits are
	// prose a person applies; a few are settings with a value, and only those
	// can be applied while nobody is watching.
	Change *Change `json:"change,omitempty"`
}

// Record is one candidate's proposal and what measuring it established.
type Record struct {
	Round int
	Label string
	Edits []Edit
	// Verdict is the selection verdict; Accepted is whether this candidate
	// became the incumbent. They differ: a NO_WORSE candidate is admissible
	// and may still lose to a better one in the same round.
	Verdict       string
	ScoreDelta    float64
	CostDelta     float64
	Accepted      bool
	Score         float64
	ScreenRefusal string
}

// Measured reports whether this record carries a measurement. A candidate the
// screen refused has a verdict and no numbers, and counting it as evidence
// about its component would let a wall of refusals look like a wall of
// failures.
//
// An empty verdict is the same hazard from the other end: a proposal recorded
// when it was drafted has not been judged yet, and reading it as measured and
// not accepted would file it as falsified before anything was run.
func (r Record) Measured() bool {
	return r.ScreenRefusal == "" && r.Verdict != "" && r.Verdict != VerdictUnjudgeable
}

// Pending reports whether a proposal is recorded and still waiting on its
// measurement. Those are the records a round must not draw conclusions from.
func (r Record) Pending() bool { return r.ScreenRefusal == "" && r.Verdict == "" }

// History is every proposal a project has made, oldest first.
type History []Record

// Tried is the components any proposal has touched, measured or not.
//
// Refused candidates count: the run did reach for that component, and
// directing it there again as "untried" would send it back to something it has
// already been told it cannot do.
func (h History) Tried() []string {
	seen := map[string]bool{}
	for _, record := range h {
		for _, edit := range record.Edits {
			seen[edit.Component] = true
		}
	}
	return sortedKeys(seen)
}

// Untried is the components nothing has reached for.
func (h History) Untried() []string {
	tried := map[string]bool{}
	for _, component := range h.Tried() {
		tried[component] = true
	}
	var untried []string
	for _, component := range Components {
		if !tried[component] {
			untried = append(untried, component)
		}
	}
	return untried
}

// Falsified is the hypotheses that were measured and did not hold, per
// component.
//
// This is what the history is for. A search that does not remember which
// explanations were tested keeps drawing the most plausible one, and the most
// plausible one is exactly what gets tried first and fails first.
func (h History) Falsified() map[string][]string {
	falsified := map[string][]string{}
	held := map[string]bool{}
	for _, record := range h {
		for _, edit := range record.Edits {
			key := hypothesisKey(edit.Component, edit.Hypothesis)
			if record.Accepted {
				held[key] = true
			}
		}
	}
	for _, record := range h {
		if !record.Measured() || record.Accepted {
			continue
		}
		for _, edit := range record.Edits {
			key := hypothesisKey(edit.Component, edit.Hypothesis)
			if held[key] {
				// The same explanation held somewhere else. One failure does
				// not falsify a hypothesis that has also been confirmed.
				continue
			}
			if !contains(falsified[edit.Component], edit.Hypothesis) {
				falsified[edit.Component] = append(falsified[edit.Component], edit.Hypothesis)
			}
		}
	}
	return falsified
}

// RecentBestGain is the best score change any recent proposal on a component
// achieved. It is negative infinity when nothing recent touched it.
func (h History) RecentBestGain(component string, window int) float64 {
	best := math.Inf(-1)
	start := 0
	if window > 0 && len(h) > window {
		start = len(h) - window
	}
	for _, record := range h[start:] {
		if !record.Measured() {
			continue
		}
		for _, edit := range record.Edits {
			if edit.Component == component && record.ScoreDelta > best {
				best = record.ScoreDelta
			}
		}
	}
	return best
}

// PruneSet is the components whose recent proposals have stopped helping.
//
// A component that has been pushed on repeatedly without gain is machinery the
// search is maintaining for nothing. Naming it is what lets the next proposal
// remove rather than extend it.
func (h History) PruneSet(window int) []string {
	var prune []string
	for _, component := range Components {
		gain := h.RecentBestGain(component, window)
		if !math.IsInf(gain, -1) && gain <= 0 {
			prune = append(prune, component)
		}
	}
	return prune
}

// NovelComponents counts structural components a candidate touches that no
// accepted proposal has touched.
func (h History) NovelComponents(edits []Edit) int {
	accepted := map[string]bool{}
	for _, record := range h {
		if !record.Accepted {
			continue
		}
		for _, edit := range record.Edits {
			accepted[edit.Component] = true
		}
	}
	novel := map[string]bool{}
	for _, edit := range edits {
		if contains(Structural, edit.Component) && !accepted[edit.Component] {
			novel[edit.Component] = true
		}
	}
	return len(novel)
}

// EditBudget is how many independent edits one candidate may bundle in round t
// of a run of total rounds.
//
// It anneals from generous to sparse on a cosine. Early rounds may bundle
// several coordinated changes, because the first moves are usually a set of
// things that only work together. Late rounds are sparse so that what is left
// is attributable: a candidate carrying one edit and a measurement says which
// edit the measurement is about.
func EditBudget(round, total, minEdits, maxEdits int) int {
	if total <= 0 {
		return maxEdits
	}
	if round < 0 {
		round = 0
	}
	if round > total {
		round = total
	}
	value := float64(minEdits) + float64(maxEdits-minEdits)*0.5*
		(1.0+math.Cos(math.Pi*float64(round)/float64(total)))
	return int(math.Ceil(roundTo(value, 9)))
}

// Stalled reports whether the score has failed to move by more than the noise
// band over the last window rounds.
//
// It returns false until there are enough rounds to look back over. A run that
// has not had time to move is not stalled, and telling it that it is would
// redirect it before it had tried anything.
func Stalled(trajectory []float64, window int, band float64) bool {
	if window <= 0 || len(trajectory) <= window {
		return false
	}
	latest := trajectory[len(trajectory)-1]
	earlier := trajectory[len(trajectory)-1-window]
	return latest-earlier <= band
}

// Direction is what the search should do next.
type Direction struct {
	// Budget is how many edits a candidate may bundle this round.
	Budget int
	// Stalled says the score has not moved beyond the noise band.
	Stalled bool
	// Explore is the components to reach for. When the run is stalled and
	// something has never been tried, exploring it is the only move that can
	// produce information the run does not already have.
	Explore []string
	// Prune is the components that have stopped paying for themselves.
	Prune []string
	// Avoid is the hypotheses already falsified, per component.
	Avoid   map[string][]string
	Summary string
}

// Next works out the direction for a round.
func Next(history History, trajectory []float64, round, total, minEdits, maxEdits, window int, band float64) Direction {
	direction := Direction{Budget: EditBudget(round, total, minEdits, maxEdits),
		Stalled: Stalled(trajectory, window, band), Avoid: history.Falsified(),
		Prune: history.PruneSet(window)}
	untried := history.Untried()
	if direction.Stalled && len(untried) > 0 {
		direction.Explore = untried
	}
	direction.Summary = describe(direction, untried)
	return direction
}

func describe(direction Direction, untried []string) string {
	parts := []string{fmt.Sprintf("이번 회차 편집 한도 %d개", direction.Budget)}
	switch {
	case len(direction.Explore) > 0:
		parts = append(parts, fmt.Sprintf("정체 상태 — 한 번도 건드리지 않은 %s 를 우선 시도하세요",
			strings.Join(direction.Explore, ", ")))
	case direction.Stalled:
		parts = append(parts, "정체 상태이고 모든 구성을 한 번은 시도했습니다")
	case len(untried) > 0:
		parts = append(parts, fmt.Sprintf("아직 안 건드린 구성: %s", strings.Join(untried, ", ")))
	}
	if len(direction.Prune) > 0 {
		parts = append(parts, fmt.Sprintf("값을 못 하고 있는 구성: %s", strings.Join(direction.Prune, ", ")))
	}
	if len(direction.Avoid) > 0 {
		parts = append(parts, fmt.Sprintf("반증된 가설 %d건은 다시 쓰지 마세요", countHypotheses(direction.Avoid)))
	}
	return join(parts...)
}

func countHypotheses(avoid map[string][]string) int {
	total := 0
	for _, list := range avoid {
		total += len(list)
	}
	return total
}

func hypothesisKey(component, hypothesis string) string {
	return strings.ToLower(strings.TrimSpace(component)) + "\x00" +
		strings.Join(strings.Fields(strings.ToLower(hypothesis)), " ")
}

func contains(list []string, value string) bool {
	for _, entry := range list {
		if strings.EqualFold(strings.TrimSpace(entry), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func roundTo(value float64, places int) float64 {
	factor := math.Pow(10, float64(places))
	return math.Round(value*factor) / factor
}
