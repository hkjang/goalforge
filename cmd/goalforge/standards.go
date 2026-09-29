package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/observer"
	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// packByRef resolves the pack a profile pins. There is one shipped pack; a
// profile naming anything else is refused rather than silently judged against
// the one that happens to be compiled in.
func packByRef(ref string) (standards.Pack, error) {
	shipped := standards.GoReactOfflineService()
	if ref == "" || ref == shipped.Ref() {
		return shipped, nil
	}
	return standards.Pack{}, fmt.Errorf("%q 팩을 알지 못합니다 — 현재 제공되는 것은 %s 입니다", ref, shipped.Ref())
}

// standardsProfile sets which pack a project is held to and what it has
// declared about itself.
func standardsProfile(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("standards profile", flag.ContinueOnError)
	packRef := set.String("pack", "", "적용할 개발팩 (기본: 제공되는 팩)")
	attributes := set.String("attributes", "", "frontend=react,network=offline 형식의 프로젝트 속성")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	pack, err := packByRef(*packRef)
	if err != nil {
		return err
	}
	existing, _, err := s.StandardProfile(ctx, project.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	profile := standards.Profile{ProjectID: project.ID, PackRef: pack.Ref(),
		Attributes: existing.Attributes, Exceptions: existing.Exceptions}
	if profile.Attributes == nil {
		profile.Attributes = map[string]string{}
	}
	for _, pair := range strings.Split(*attributes, ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("%q 는 key=value 형식이 아닙니다", pair)
		}
		profile.Attributes[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if err = s.SaveStandardProfile(ctx, profile, pack); err != nil {
		return err
	}
	inForce := standards.InForce(pack, profile, time.Now())
	fmt.Printf("적용: %s — 기준 %d개 중 %d개가 이 프로젝트에 적용됩니다\n",
		pack.Ref(), len(pack.Standards), len(inForce))
	return nil
}

// standardsExcept records a decision not to apply a criterion.
func standardsExcept(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("standards except", flag.ContinueOnError)
	standardID := set.String("standard", "", "제외할 기준 ID")
	reason := set.String("reason", "", "제외 사유")
	decider := set.String("decider", "", "결정자")
	reviewWhen := set.String("review-when", "", "다시 볼 조건")
	reviewBy := set.String("review-by", "", "다시 볼 날짜 (2006-01-02)")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	profile, _, err := s.StandardProfile(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("먼저 `goalforge standards profile` 로 적용할 팩을 정하세요: %w", err)
	}
	pack, err := packByRef(profile.PackRef)
	if err != nil {
		return err
	}
	exception := standards.Exception{StandardID: strings.ToUpper(strings.TrimSpace(*standardID)),
		Reason: *reason, Decider: *decider, ReviewWhen: *reviewWhen, DecidedAt: time.Now().UTC()}
	if *reviewBy != "" {
		if exception.ReviewBy, err = time.Parse("2006-01-02", *reviewBy); err != nil {
			return fmt.Errorf("--review-by 는 2006-01-02 형식입니다: %w", err)
		}
	}
	kept := profile.Exceptions[:0]
	for _, existing := range profile.Exceptions {
		if existing.StandardID != exception.StandardID {
			kept = append(kept, existing)
		}
	}
	profile.Exceptions = append(kept, exception)
	if err = s.SaveStandardProfile(ctx, profile, pack); err != nil {
		return err
	}
	fmt.Printf("%s 를 적용 제외로 기록했습니다 (%s)\n", exception.StandardID, exception.Decider)
	return nil
}

// standardsStatus prints where every criterion in force stands.
func standardsStatus(ctx context.Context, s *store.Store) error {
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	profile, _, err := s.StandardProfile(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("먼저 `goalforge standards profile` 로 적용할 팩을 정하세요: %w", err)
	}
	pack, err := packByRef(profile.PackRef)
	if err != nil {
		return err
	}
	drifted, err := s.PackDrift(ctx, project.ID, pack)
	if err != nil {
		return err
	}
	if drifted {
		fmt.Printf("경고: 고정한 %s 의 내용이 바뀌었습니다 — 동의한 적 없는 기준으로 판정되고 있을 수 있습니다\n\n", pack.Ref())
	}
	views, err := s.AssessmentsFor(ctx, project.ID, pack, profile, time.Now())
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, view := range views {
		counts[view.Result]++
	}
	fmt.Printf("%s — 적용 %d건\n", pack.Ref(), len(views))
	fmt.Printf("충족 %d · 미충족 %d · 일부 %d · 미확인 %d · 적용 제외 %d\n\n",
		counts[standards.ResultMet], counts[standards.ResultUnmet], counts[standards.ResultPartial],
		counts[standards.ResultUnknown], counts[standards.ResultNotApplicable])
	// Unmet first, then partial, then unknown. What is known to be wrong comes
	// before what nobody has looked at, and both come before what is settled.
	sort.SliceStable(views, func(i, j int) bool {
		return resultOrder(views[i].Result) < resultOrder(views[j].Result)
	})
	for _, view := range views {
		mark := resultMark(view.Result)
		stale := ""
		if view.Stale {
			stale = " (기준 개정 이후 재평가 필요)"
		}
		fmt.Printf("%s  %-10s %-8s %s%s\n", mark, view.StandardID, view.Severity, view.Title, stale)
		if view.Detail != "" {
			fmt.Printf("               %s\n", view.Detail)
		}
	}
	return nil
}

func resultOrder(result string) int {
	switch result {
	case standards.ResultUnmet:
		return 0
	case standards.ResultPartial:
		return 1
	case standards.ResultUnknown:
		return 2
	case standards.ResultMet:
		return 3
	}
	return 4
}

func resultMark(result string) string {
	switch result {
	case standards.ResultMet:
		return "[v]"
	case standards.ResultUnmet:
		return "[!]"
	case standards.ResultPartial:
		return "[~]"
	case standards.ResultNotApplicable:
		return "[-]"
	}
	return "[?]"
}

// standardsPass runs a pass only when there is a reason to.
//
// It is what a schedule or a hook calls. Unlike `assess`, which does what it
// is told, this decides — and says what it decided when it decides not to,
// because "nothing changed" and "the budget is gone" look identical from
// outside and call for completely different responses.
func standardsPass(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("standards pass", flag.ContinueOnError)
	interval := set.Duration("interval", 24*time.Hour, "아무 일이 없어도 다시 볼 주기")
	floor := set.Int("backlog-floor", 3, "실행 가능한 대기 작업이 이보다 적으면 평가한다")
	budget := set.Int("daily-budget", 4, "하루에 허용할 평가 횟수 (구현 예산과 별개)")
	perRun := set.Int("supply-limit", 3, "한 번에 공급할 최대 작업 수")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, profile, pack, err := standardsContext(ctx, s)
	if err != nil {
		return err
	}
	head, err := gitops.HeadCommit(ctx, project.RepositoryPath, project.DefaultBranch)
	if err != nil {
		return err
	}
	goal, err := s.CurrentGoal(ctx, project.ID)
	if err != nil {
		return err
	}
	result, err := observer.RunScheduledPass(ctx, s, observer.PassRequest{
		ProjectID: project.ID, GoalID: goal.ID, Repository: project.RepositoryPath, HeadSHA: head,
		ToolVersion: version, Pack: pack, Profile: profile, Detectors: observer.Default(),
		Schedule: observer.SchedulePolicy{Interval: *interval, BacklogFloor: *floor, DiscoveryBudget: *budget},
		Supply:   observer.SupplyPolicy{PerRun: *perRun, MaxOutstanding: 10}})
	if errors.Is(err, observer.ErrDiscoveryBudgetSpent) {
		fmt.Printf("발견 예산을 다 썼습니다: %v\n", err)
		return nil
	}
	if errors.Is(err, observer.ErrSupplyPaused) {
		fmt.Printf("공급을 멈췄습니다: %v\n", err)
		return nil
	}
	if err != nil {
		return err
	}
	if !result.Ran {
		fmt.Printf("평가하지 않았습니다: %s\n", result.Detail)
		return nil
	}
	fmt.Printf("평가했습니다 (%s): %s\n", result.Decision.Trigger, result.Decision.Reason)
	if len(result.Result.Filed) > 0 {
		fmt.Printf("공급: %s\n", strings.Join(result.Result.Filed, ", "))
	}
	if len(result.Result.Deferred) > 0 {
		fmt.Printf("보류(다음 회차): %s\n", strings.Join(result.Result.Deferred, ", "))
	}
	return nil
}

// standardsContext loads the three things every standards command needs.
func standardsContext(ctx context.Context, s *store.Store) (model.Project, standards.Profile, standards.Pack, error) {
	project, err := currentProject(ctx, s)
	if err != nil {
		return project, standards.Profile{}, standards.Pack{}, err
	}
	profile, _, err := s.StandardProfile(ctx, project.ID)
	if err != nil {
		return project, profile, standards.Pack{},
			fmt.Errorf("먼저 `goalforge standards profile` 로 적용할 팩을 정하세요: %w", err)
	}
	pack, err := packByRef(profile.PackRef)
	return project, profile, pack, err
}

// standardsAssess reads the repository at a commit and files what is missing.
func standardsAssess(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("standards assess", flag.ContinueOnError)
	commit := set.String("commit", "", "평가할 커밋 (기본: 기본 브랜치의 HEAD)")
	supply := set.Bool("supply", false, "미충족 기준을 작업으로 공급한다")
	perRun := set.Int("supply-limit", 3, "한 번에 공급할 최대 작업 수")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	profile, _, err := s.StandardProfile(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("먼저 `goalforge standards profile` 로 적용할 팩을 정하세요: %w", err)
	}
	pack, err := packByRef(profile.PackRef)
	if err != nil {
		return err
	}
	sha := *commit
	if sha == "" {
		// Pinned to a commit rather than read from the working tree: a
		// working-tree read cannot be reproduced and includes files nobody
		// committed.
		if sha, err = gitops.HeadCommit(ctx, project.RepositoryPath, project.DefaultBranch); err != nil {
			return err
		}
	}
	goal, err := s.CurrentGoal(ctx, project.ID)
	if err != nil {
		return err
	}
	policy := observer.SupplyPolicy{PerRun: *perRun, MaxOutstanding: 10}
	if !*supply {
		// Assess without filing anything. Reading a repository should not put
		// work on someone's board unless they asked for it.
		policy.PerRun = 0
		policy.MaxOutstanding = -1
	}
	result, err := observer.ObserveAndSupply(ctx, s, project.ID, goal.ID, project.RepositoryPath, sha,
		version, pack, profile, observer.Default(), policy)
	if errors.Is(err, observer.ErrSupplyPaused) {
		fmt.Printf("공급을 멈췄습니다: %v\n", err)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("커밋 %s 기준으로 평가했습니다\n", shortSHA(sha))
	if *supply {
		if len(result.Filed) > 0 {
			fmt.Printf("공급: %s\n", strings.Join(result.Filed, ", "))
		}
		for standardID, workID := range result.AlreadyFiled {
			fmt.Printf("이미 접수됨: %s → %s\n", standardID, workID)
		}
		if len(result.Deferred) > 0 {
			fmt.Printf("보류(다음 회차): %s\n", strings.Join(result.Deferred, ", "))
		}
	}
	if len(result.Unchecked) > 0 {
		// Said plainly rather than left out. A report that only lists what was
		// examined reads as coverage of a catalogue nobody walked.
		fmt.Printf("자동 검사 없음 %d건: %s\n", len(result.Unchecked), strings.Join(result.Unchecked, ", "))
	}
	fmt.Println("`goalforge standards status` 로 전체 현황을 봅니다")
	return nil
}
