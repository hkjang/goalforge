package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

// The context package is what a fresh session is handed instead of having to
// re-derive settled decisions and repeat fixes that already failed.
func TestBuildContextPackage(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	project := model.Project{ID: "P1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, project.ID, "goal", "objective", "", []model.Criterion{{Type: "coverage", ExpectedValue: "85"}})
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateWorkItem(ctx, model.WorkItem{ID: "W1", GoalID: goal.ID, Type: "IMPLEMENT", Title: "feature",
		ChangeScope: "internal/api/**", Acceptance: "로그인 성공과 실패가 모두 테스트된다"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordDecision(ctx, DesignDecision{ProjectID: project.ID, Title: "세션 저장소",
		Decision: "세션은 SQLite 에 보관한다", Alternatives: "메모리: 재시작에 살아남지 못함", BaseCommit: "abcdef1234567890"}); err != nil {
		t.Fatal(err)
	}
	superseded, err := s.RecordDecision(ctx, DesignDecision{ProjectID: project.ID, Title: "구버전", Decision: "메모리 저장"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SupersedeDecision(ctx, project.ID, superseded.ID, "DEC-NEW"); err != nil {
		t.Fatal(err)
	}
	if err = s.UpsertGate(ctx, project.ID, GateConfig{Type: "coverage", Command: []string{"go", "test", "-cover", "./..."},
		Timeout: time.Minute, Required: true, SuccessValue: "85", ValuePattern: `([0-9.]+)%`}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,project_id,work_item_id,provider,state,started_at) VALUES('R1',?,'W1','codex','REPAIR_REQUIRED',?)`,
		project.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,run_id,check_type,status,actual_value,required,failure_kind,output,created_at) VALUES(?,'R1','coverage','FAILED','71.4',1,'threshold_not_met','measured 71.4 is below the threshold 85',?)`,
		goal.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	pkg, err := s.BuildContextPackage(ctx, project, goal, work)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Empty() {
		t.Fatal("context package is empty")
	}
	// Superseded decisions are not handed to a session as current.
	if len(pkg.Decisions) != 1 || !strings.Contains(pkg.Decisions[0].Body, "메모리: 재시작") {
		t.Fatalf("decisions=%+v", pkg.Decisions)
	}
	if !strings.Contains(pkg.Decisions[0].Source, "abcdef123456") {
		t.Fatalf("a decision must carry the commit it was made against: %+v", pkg.Decisions[0])
	}
	if len(pkg.PastFailures) != 1 || !strings.Contains(pkg.PastFailures[0].Source, "threshold_not_met") {
		t.Fatalf("past failures=%+v", pkg.PastFailures)
	}
	var scoped, acceptance bool
	for _, constraint := range pkg.Constraints {
		if strings.Contains(constraint.Body, "internal/api/**") {
			scoped = true
		}
		if strings.Contains(constraint.Body, "로그인 성공") {
			acceptance = true
		}
	}
	if !scoped || !acceptance {
		t.Fatalf("constraints=%+v", pkg.Constraints)
	}
	var hasGate, hasCriterion bool
	for _, item := range pkg.Verification {
		if item.Kind == "gate" && strings.Contains(item.Body, "go test -cover") {
			hasGate = true
		}
		if item.Kind == "criterion" && strings.Contains(item.Body, "기준 미달") {
			hasCriterion = true
		}
	}
	if !hasGate || !hasCriterion {
		t.Fatalf("verification=%+v", pkg.Verification)
	}
}
