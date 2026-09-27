package evaluation

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// ConditionHash identifies what a trial was run under, so results are only
// ever compared against results produced the same way. A comparison across
// different budgets, gates, or fixtures is not a comparison.
func ConditionHash(spec CaseSpec, label string) string {
	gates := append([]Gate(nil), spec.Gates...)
	sort.Slice(gates, func(i, j int) bool { return gates[i].Type < gates[j].Type })
	criteria := append([]Criterion(nil), spec.Criteria...)
	sort.Slice(criteria, func(i, j int) bool { return criteria[i].Type < criteria[j].Type })
	payload := struct {
		Label, Fixture, Ref, CleanTreeID string
		Criteria                         []Criterion
		Gates                            []Gate
		SeedWork                         []SeedWorkItem
		TokenBudget                      int64
		CostBudgetUSD                    float64
		TimeoutSeconds                   int
	}{label, spec.Fixture, spec.Ref, spec.CleanTreeID, criteria, gates, spec.SeedWork, spec.TokenBudget, spec.CostBudgetUSD, spec.TimeoutSeconds}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))[:16]
}
