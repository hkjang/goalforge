package planner

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/goalforge/goalforge/internal/model"
)

var ErrImplementationPreferred = errors.New("unimplemented backlog limit reached; implementation is preferred")

type Candidate struct {
	Title               string  `json:"title"`
	ExpectedChangeScope string  `json:"expected_change_scope"`
	Risk                string  `json:"risk"`
	GoalContribution    float64 `json:"goal_contribution"`
	UserValue           float64 `json:"user_value"`
	OperationalNeed     float64 `json:"operational_need"`
	Feasibility         float64 `json:"feasibility"`
	RiskReduction       float64 `json:"risk_reduction"`
	Difficulty          float64 `json:"difficulty"`
	ScopeExpansion      bool    `json:"scope_expansion"`
}
type Policy struct {
	MaxNewIdeas, MaxUnimplemented         int
	DuplicateThreshold, LowScoreThreshold float64
}

func DefaultPolicy() Policy {
	return Policy{MaxNewIdeas: 5, MaxUnimplemented: 10, DuplicateThreshold: .72, LowScoreThreshold: 35}
}

type Accepted struct {
	Candidate Candidate
	Score     model.IdeaScore
	Status    string
}
type DiscoveryResult struct {
	Accepted []Accepted
	Rejected map[string]string
}

func Discover(policy Policy, existing []model.WorkItem, candidates []Candidate) (DiscoveryResult, error) {
	result := DiscoveryResult{Rejected: make(map[string]string)}
	unimplemented := 0
	known := make([]string, 0, len(existing))
	for _, item := range existing {
		known = append(known, item.Title)
		if item.Status == "BACKLOG" || item.Status == "APPROVED" || item.Status == "IN_PROGRESS" || item.Status == "VERIFYING" {
			unimplemented++
		}
	}
	if unimplemented >= policy.MaxUnimplemented {
		return result, ErrImplementationPreferred
	}
	for _, candidate := range candidates {
		if len(result.Accepted) >= policy.MaxNewIdeas {
			result.Rejected[candidate.Title] = "cycle idea limit reached"
			continue
		}
		if err := validateCandidate(candidate); err != nil {
			result.Rejected[candidate.Title] = err.Error()
			continue
		}
		duplicate := ""
		for _, title := range known {
			if Similarity(candidate.Title, title) >= policy.DuplicateThreshold {
				duplicate = title
				break
			}
		}
		if duplicate != "" {
			result.Rejected[candidate.Title] = "duplicates existing item: " + duplicate
			continue
		}
		score := Score(candidate)
		status := "BACKLOG"
		if candidate.ScopeExpansion || score.PriorityScore < policy.LowScoreThreshold {
			status = "BLOCKED"
		}
		result.Accepted = append(result.Accepted, Accepted{Candidate: candidate, Score: score, Status: status})
		known = append(known, candidate.Title)
	}
	sort.SliceStable(result.Accepted, func(i, j int) bool {
		return result.Accepted[i].Score.PriorityScore > result.Accepted[j].Score.PriorityScore
	})
	return result, nil
}

func Score(c Candidate) model.IdeaScore {
	return model.IdeaScore{GoalContribution: c.GoalContribution, UserValue: c.UserValue, OperationalNeed: c.OperationalNeed, Feasibility: c.Feasibility, RiskReduction: c.RiskReduction, Difficulty: c.Difficulty, PriorityScore: c.GoalContribution*.30 + c.UserValue*.25 + c.OperationalNeed*.20 + c.Feasibility*.15 + c.RiskReduction*.10, ExpectedChangeScope: c.ExpectedChangeScope, Fingerprint: Fingerprint(c.Title), ScopeExpansion: c.ScopeExpansion, ApprovalRequired: c.ScopeExpansion}
}
func validateCandidate(c Candidate) error {
	if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.ExpectedChangeScope) == "" {
		return errors.New("title and expected change scope are required")
	}
	for name, value := range map[string]float64{"goal contribution": c.GoalContribution, "user value": c.UserValue, "operational need": c.OperationalNeed, "feasibility": c.Feasibility, "risk reduction": c.RiskReduction, "difficulty": c.Difficulty} {
		if value < 0 || value > 100 {
			return fmt.Errorf("%s must be between 0 and 100", name)
		}
	}
	return nil
}
func Fingerprint(text string) string { return strings.Join(ngrams(normalize(text)), "|") }
func Similarity(a, b string) float64 {
	left, right := ngrams(normalize(a)), ngrams(normalize(b))
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	ls := make(map[string]struct{}, len(left))
	for _, v := range left {
		ls[v] = struct{}{}
	}
	rs := make(map[string]struct{}, len(right))
	for _, v := range right {
		rs[v] = struct{}{}
	}
	intersection := 0
	for v := range ls {
		if _, ok := rs[v]; ok {
			intersection++
		}
	}
	union := len(ls) + len(rs) - intersection
	return float64(intersection) / float64(union)
}
func normalize(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func ngrams(value string) []string {
	runes := []rune(value)
	if len(runes) == 0 {
		return nil
	}
	if len(runes) < 3 {
		return []string{string(runes)}
	}
	out := make([]string, 0, len(runes)-2)
	for i := 0; i <= len(runes)-3; i++ {
		out = append(out, string(runes[i:i+3]))
	}
	return out
}
