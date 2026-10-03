package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/observer"
	"github.com/goalforge/goalforge/internal/rrsi"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
	"github.com/goalforge/goalforge/internal/verification"
)

// goalDraft turns a topic into completion criteria and the gates that would
// settle them, for a person to confirm.
//
// The confirmation is the point. A machine that sets its own bar has not been
// measured against anything — it has agreed with itself — so this writes
// nothing until somebody says yes.
func goalDraft(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("goal draft", flag.ContinueOnError)
	topic := set.String("topic", "", "무엇을 만들 것인지")
	repairs := set.Int("repairs", 2, "거절된 초안을 다시 쓰게 할 최대 횟수")
	apply := set.Bool("apply", false, "확인했으니 목표와 게이트를 실제로 설정한다")
	skipTrial := set.Bool("skip-trial", false, "제안된 게이트를 지금 돌려 보지 않는다")
	if err := set.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*topic) == "" {
		return errors.New("--topic 이 필요합니다")
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	providers, cleanup, err := workerProviders(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	chosen, err := pickProvider(providers, project.Provider)
	if err != nil {
		return err
	}
	summary, err := repositorySummary(ctx, project)
	if err != nil {
		return err
	}
	existing, err := s.ListGates(ctx, project.ID)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(existing))
	for _, gate := range existing {
		names = append(names, gate.Type)
	}
	sort.Strings(names)
	request := observer.DraftRequest{Topic: *topic, Repository: project.RepositoryPath,
		RepositorySummary: summary, ExistingGates: names}
	if !*skipTrial {
		// The gates are tried against the current tree. A gate for work not
		// yet done that passes today will pass tomorrow, and the criterion it
		// settles is satisfied before anybody writes anything.
		engine, engineErr := verification.New(s, 1<<20)
		if engineErr != nil {
			return engineErr
		}
		request.Engine = engine
	}
	author := observer.Author{Provider: chosen, Model: project.Model, Repairs: *repairs}
	draft, err := observer.DraftGoal(ctx, author, request)
	if err != nil {
		return err
	}
	if !draft.Accepted() {
		fmt.Printf("초안 %d회 시도했으나 받아들일 수 없습니다:\n%s\n", draft.Attempts, rrsi.Explain(draft.Refusals))
		return errors.New("확인할 수 있는 초안이 없습니다")
	}
	printDraft(draft)
	if !*apply {
		fmt.Println("\n이대로 설정하려면 같은 명령에 --apply 를 붙이세요. 확인은 사람이 합니다 —")
		fmt.Println("자기 합격선을 스스로 정한 기계는 무엇과도 비교된 적이 없고, 자기와 합의했을 뿐입니다.")
		return nil
	}
	return applyDraft(ctx, s, project, draft)
}

func printDraft(draft observer.GoalDraft) {
	fmt.Printf("제목: %s\n목적: %s\n\n완료 조건 %d개 (%d회 시도):\n",
		draft.Title, draft.Objective, len(draft.Criteria), draft.Attempts)
	for _, criterion := range draft.Criteria {
		state := "지금 실패함"
		if !criterion.FailsNow {
			state = "지금 돌려 보지 않음"
		}
		fmt.Printf("  %s %s  [%s]\n", criterion.Type, criterion.ExpectedValue, criterion.Kind)
		fmt.Printf("       게이트: %s  (%s)\n", strings.Join(criterion.GateCommand, " "), state)
		fmt.Printf("       %s\n", criterion.WhyItFailsNow)
	}
	// Shown because it is the one gate that will fail a run. The confirmation
	// is the point of this command, and asking somebody to confirm a draft
	// without showing the thing that blocks every run is asking them to
	// confirm what they cannot see.
	if draft.Health != nil {
		state := "지금 통과함"
		if draft.Health.FailsNow {
			state = "지금 실패함 — 이대로면 모든 실행이 막힙니다"
		}
		fmt.Printf("\n매 실행 뒤 통과해야 하는 게이트 (이것만 실행을 막습니다):\n")
		fmt.Printf("  %s %s  [%s]\n", draft.Health.Type, draft.Health.ExpectedValue, draft.Health.Kind)
		fmt.Printf("       게이트: %s  (%s)\n", strings.Join(draft.Health.GateCommand, " "), state)
		fmt.Printf("       %s\n", draft.Health.WhyItFailsNow)
		fmt.Println("\n완료 조건 게이트는 측정만 합니다 — 부분 작업이 전부 실패하지 않게 하려면 그래야 합니다")
	}
}

// applyDraft writes the goal and its gates.
//
// The gates go in first. A goal whose criteria have no gates is one the runner
// refuses to work on, and writing it alone would leave the project in a state
// that looks configured and cannot run.
func applyDraft(ctx context.Context, s *store.Store, project model.Project, draft observer.GoalDraft) error {
	// Only the health gate is required; the completion criteria are measured.
	// Which is which is Gates' decision, so this cannot disagree with it.
	gates := draft.Gates()
	for _, gate := range gates {
		if err := s.UpsertGate(ctx, project.ID, gate); err != nil {
			return err
		}
	}
	criteria := make([]model.Criterion, 0, len(draft.Criteria))
	for _, criterion := range draft.Criteria {
		criteria = append(criteria, model.Criterion{Type: criterion.Type,
			ExpectedValue: criterion.ExpectedValue, RequiredKind: criterion.Kind})
	}
	goal, err := s.SetGoal(ctx, project.ID, draft.Title, draft.Objective, "확인된 초안", criteria)
	if err != nil {
		return err
	}
	fmt.Printf("\n설정했습니다: %s v%d · 게이트 %d개 (매 실행을 막는 것은 %s 하나)\n",
		goal.Title, goal.Version, len(gates), draft.Health.Type)
	fmt.Println("완료 조건 게이트는 측정만 합니다 — 부분 작업이 전부 실패하지 않게 하려면 그래야 합니다")
	fmt.Println("`goalforge worker` 가 이제 이 목표를 향해 돌 수 있습니다")
	return nil
}

// repositorySummary is what the drafter is shown about the repository.
//
// A listing rather than the contents: the question is what kind of project
// this is, and a model given every file will write criteria about whichever
// file it read last.
func repositorySummary(ctx context.Context, project model.Project) (string, error) {
	head, err := gitops.HeadCommit(ctx, project.RepositoryPath, project.DefaultBranch)
	if err != nil {
		return "", err
	}
	paths, err := gitops.ListTree(ctx, project.RepositoryPath, head)
	if err != nil {
		return "", err
	}
	tops := map[string]int{}
	for _, path := range paths {
		head := path
		if index := strings.IndexByte(path, '/'); index >= 0 {
			head = path[:index] + "/"
		}
		tops[head]++
	}
	entries := make([]string, 0, len(tops))
	for name, count := range tops {
		entries = append(entries, fmt.Sprintf("%s (%d)", name, count))
	}
	sort.Strings(entries)
	if len(entries) > 40 {
		entries = entries[:40]
	}
	return strings.Join(entries, ", "), nil
}
