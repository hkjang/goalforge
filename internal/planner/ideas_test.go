package planner

import (
	"errors"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func candidate(title string) Candidate {
	return Candidate{Title: title, ExpectedChangeScope: "internal/session", GoalContribution: 90, UserValue: 80, OperationalNeed: 70, Feasibility: 60, RiskReduction: 50, Difficulty: 40}
}

func TestScoreUsesRequiredWeights(t *testing.T) {
	score := Score(candidate("세션 재개 구현"))
	want := 90*.30 + 80*.25 + 70*.20 + 60*.15 + 50*.10
	if score.PriorityScore != want {
		t.Fatalf("score=%v want=%v", score.PriorityScore, want)
	}
}
func TestDiscoverRejectsExistingAndCycleDuplicates(t *testing.T) {
	existing := []model.WorkItem{{Title: "세션 재개 기능 구현", Status: "DONE"}, {Title: "감사 로그", Status: "DISCARDED"}}
	result, err := Discover(DefaultPolicy(), existing, []Candidate{candidate("세션 재개 기능 구현"), candidate("사용량 대시보드"), candidate("사용량 대시보드")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted) != 1 || result.Accepted[0].Candidate.Title != "사용량 대시보드" {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Rejected) != 2 {
		t.Fatalf("rejected=%v", result.Rejected)
	}
}
func TestScopeExpansionRequiresApproval(t *testing.T) {
	c := candidate("외부 배포 시스템 추가")
	c.ScopeExpansion = true
	result, err := Discover(DefaultPolicy(), nil, []Candidate{c})
	if err != nil {
		t.Fatal(err)
	}
	accepted := result.Accepted[0]
	if accepted.Status != "BLOCKED" || !accepted.Score.ApprovalRequired {
		t.Fatalf("accepted=%+v", accepted)
	}
}
func TestBacklogLimitPrefersImplementation(t *testing.T) {
	items := make([]model.WorkItem, 10)
	for i := range items {
		items[i] = model.WorkItem{Title: string(rune('a' + i)), Status: "BACKLOG"}
	}
	_, err := Discover(DefaultPolicy(), items, []Candidate{candidate("new")})
	if !errors.Is(err, ErrImplementationPreferred) {
		t.Fatalf("err=%v", err)
	}
}
func TestIdeaCycleLimit(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxNewIdeas = 2
	result, err := Discover(policy, nil, []Candidate{candidate("one"), candidate("two"), candidate("three")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted) != 2 {
		t.Fatalf("accepted=%d", len(result.Accepted))
	}
}
