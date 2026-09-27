package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func contractFixture(t *testing.T) (context.Context, *Store, model.Project) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	return ctx, s, project
}

// AT-01: "최고 수준의 서비스" is not an outcome until someone says what would
// settle it. An outcome with no method or no judge is recorded as unconfirmed
// rather than accepted as a requirement or dropped.
func TestContractMarksOutcomesNobodyCanSettle(t *testing.T) {
	ctx, s, project := contractFixture(t)
	contract, err := s.SaveContract(ctx, GoalContract{ProjectID: project.ID, Title: "최고 수준의 상담 서비스",
		Outcomes: []RequiredOutcome{
			{Key: "login", Statement: "로그인이 동작한다", Method: "gate:auth_tests", Judge: "verification"},
			{Key: "best_in_class", Statement: "업계 최고 수준이다"},
			{Key: "fast", Statement: "빠르다", Method: "gate:latency"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	unconfirmed := contract.Unconfirmed()
	if len(unconfirmed) != 2 {
		t.Fatalf("outcomes with no way to settle them must be flagged: %+v", unconfirmed)
	}
	keys := map[string]bool{}
	for _, outcome := range unconfirmed {
		keys[outcome.Key] = true
	}
	if !keys["best_in_class"] || !keys["fast"] {
		t.Fatalf("unconfirmed=%+v", unconfirmed)
	}
	// Nothing was dropped: the vague requirements are still in the contract.
	loaded, err := s.CurrentContract(ctx, project.ID)
	if err != nil || len(loaded.Outcomes) != 3 {
		t.Fatalf("an unsettleable outcome must be kept, not discarded: %+v err=%v", loaded.Outcomes, err)
	}
	for _, outcome := range loaded.Outcomes {
		if outcome.Key == "login" && outcome.Status != OutcomeConfirmed {
			t.Fatalf("a settleable outcome must be confirmed: %+v", outcome)
		}
	}
}

// AT-02: two requirements that cannot both hold are presented as a conflict.
// Quietly dropping one is how a contract becomes a different contract.
func TestContractSurfacesContradictoryRequirements(t *testing.T) {
	ctx, s, project := contractFixture(t)
	contract, err := s.SaveContract(ctx, GoalContract{ProjectID: project.ID, Title: "성능과 비용",
		Outcomes: []RequiredOutcome{
			{Key: "throughput", Statement: "초당 1000건 이상", Method: "gate:load", Judge: "verification",
				Metric: "rps", Comparator: ">=", Threshold: "1000"},
			{Key: "single_node", Statement: "초당 200건 이하의 단일 노드", Method: "gate:load", Judge: "verification",
				Metric: "rps", Comparator: "<=", Threshold: "200"},
			{Key: "uptime", Statement: "99.9% 이상", Method: "observation", Judge: "operator",
				Metric: "uptime", Comparator: ">=", Threshold: "99.9"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	conflicts := contract.Conflicts()
	if len(conflicts) != 1 {
		t.Fatalf("conflicts=%+v", conflicts)
	}
	if !strings.Contains(conflicts[0].Detail, "rps") {
		t.Fatalf("the conflict must name the metric: %q", conflicts[0].Detail)
	}
	// Both sides survive: resolving the conflict is the user's decision.
	loaded, _ := s.CurrentContract(ctx, project.ID)
	if len(loaded.Outcomes) != 3 {
		t.Fatalf("no requirement may be dropped to resolve a conflict: %+v", loaded.Outcomes)
	}
}

// A detector that guesses trains people to ignore it, so unrelated metrics and
// non-numeric statements are not reported as conflicts.
func TestContractDoesNotInventConflicts(t *testing.T) {
	ctx, s, project := contractFixture(t)
	contract, err := s.SaveContract(ctx, GoalContract{ProjectID: project.ID, Title: "무관한 조건들",
		Outcomes: []RequiredOutcome{
			{Key: "rps", Method: "gate:load", Judge: "verification", Metric: "rps", Comparator: ">=", Threshold: "1000"},
			{Key: "latency", Method: "gate:load", Judge: "verification", Metric: "latency_ms", Comparator: "<=", Threshold: "200"},
			{Key: "usable", Statement: "쓰기 편해야 한다", Method: "review", Judge: "operator"},
			{Key: "compatible", Method: "gate:load", Judge: "verification", Metric: "rps", Comparator: ">=", Threshold: "500"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if conflicts := contract.Conflicts(); len(conflicts) != 0 {
		t.Fatalf("compatible requirements must not be reported as conflicts: %+v", conflicts)
	}
}

// AT-03: narrowing the goal is a new contract version with a reason and a
// decider, and the previous version keeps what it required — so a goal that
// was missed does not become one that was met.
func TestNarrowingTheContractCannotRewriteHistory(t *testing.T) {
	ctx, s, project := contractFixture(t)
	original := GoalContract{ProjectID: project.ID, Title: "상담 시스템",
		Outcomes: []RequiredOutcome{
			{Key: "login", Method: "gate:auth", Judge: "verification"},
			{Key: "export", Method: "gate:export", Judge: "verification"},
		}}
	if _, err := s.SaveContract(ctx, original); err != nil {
		t.Fatal(err)
	}
	// Dropping a requirement without saying why or who decided is refused.
	narrowed := GoalContract{ProjectID: project.ID, Title: "상담 시스템",
		Outcomes: []RequiredOutcome{{Key: "login", Method: "gate:auth", Judge: "verification"}}}
	if _, err := s.SaveContract(ctx, narrowed); err == nil {
		t.Fatal("a contract change needs a reason and a decider")
	}
	narrowed.ChangeReason = "내보내기는 다음 분기로 미룸"
	narrowed.Decider = "hkjang"
	saved, err := s.SaveContract(ctx, narrowed)
	if err != nil || saved.Version != 2 {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	history, err := s.ContractHistory(ctx, project.ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	// Version 1 still required the export; narrowing did not reach back.
	if len(history[0].Outcomes) != 2 {
		t.Fatalf("the original contract must keep what it required: %+v", history[0].Outcomes)
	}
	if len(history[1].Outcomes) != 1 || history[1].ChangeReason == "" || history[1].Decider == "" {
		t.Fatalf("the new version must record why and who: %+v", history[1])
	}
}

// A contract with nothing required cannot be judged, so it is refused at the
// point it is written rather than at completion.
func TestContractNeedsAtLeastOneRequiredOutcome(t *testing.T) {
	ctx, s, project := contractFixture(t)
	if _, err := s.SaveContract(ctx, GoalContract{ProjectID: project.ID, Title: "빈 계약"}); err == nil {
		t.Fatal("a contract with no required outcome must be refused")
	}
}
