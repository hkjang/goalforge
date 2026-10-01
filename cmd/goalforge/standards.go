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
func packByRef(ref string) (standards.Pack, error) { return standards.ByRef(ref) }

// standardsProfile sets which pack a project is held to and what it has
// declared about itself.
func standardsProfile(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("standards profile", flag.ContinueOnError)
	packRef := set.String("pack", "", "적용할 개발팩 (기본: 속성에 가장 맞는 팩)")
	list := set.Bool("list-packs", false, "이 빌드가 가진 팩 목록")
	attributes := set.String("attributes", "", "frontend=react,network=offline 형식의 프로젝트 속성")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *list {
		for _, pack := range standards.Shipped() {
			fmt.Printf("%-34s %s (기준 %d개)\n", pack.Ref(), pack.Title, len(pack.Standards))
		}
		return nil
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	existing, _, err := s.StandardProfile(ctx, project.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	profile := standards.Profile{ProjectID: project.ID,
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
	// The pack is resolved after the attributes are known, so a project that
	// declares what it is gets the catalogue that describes it rather than
	// whichever one happens to be first.
	var pack standards.Pack
	switch {
	case *packRef != "":
		if pack, err = packByRef(*packRef); err != nil {
			return err
		}
	case existing.PackRef != "":
		if pack, err = packByRef(existing.PackRef); err != nil {
			return err
		}
	default:
		suggested, ok := standards.Suggest(profile.Attributes)
		if !ok {
			return fmt.Errorf("선언한 속성으로는 어떤 팩이 맞는지 알 수 없습니다 — `--pack` 으로 고르거나 `--list-packs` 로 목록을 보세요\n속성 예: deployment=cli,release=binaries 또는 deployment=service,frontend=react")
		}
		pack = suggested
	}
	// What a repin changed, before it is saved. A pack swapped silently leaves
	// the operator to find out from the board that the catalogue moved, and
	// the revised criteria are exactly the ones whose assessments just went
	// back to unknown.
	if existing.PackRef != "" && existing.PackRef != pack.Ref() {
		previous, prevErr := packByRef(existing.PackRef)
		if prevErr != nil {
			return fmt.Errorf("이전 팩 %s 를 읽지 못해 무엇이 달라졌는지 말할 수 없습니다: %w",
				existing.PackRef, prevErr)
		}
		diff := standards.DiffPacks(previous, pack)
		fmt.Printf("팩 교체: %s → %s\n", existing.PackRef, pack.Ref())
		if diff.Empty() {
			fmt.Println("  요구하는 것은 같습니다")
		} else {
			reportPackDiff(diff)
		}
	}
	profile.PackRef = pack.Ref()
	if err = s.SaveStandardProfile(ctx, profile, pack); err != nil {
		return err
	}
	inForce := standards.InForce(pack, profile, time.Now())
	fmt.Printf("적용: %s — 기준 %d개 중 %d개가 이 프로젝트에 적용됩니다\n",
		pack.Ref(), len(pack.Standards), len(inForce))
	return nil
}

// reportPackDiff says what a pack swap changed, in the three terms a project
// has to decide about.
func reportPackDiff(diff standards.Diff) {
	if len(diff.Added) > 0 {
		fmt.Printf("  추가 %d건: %s\n", len(diff.Added), strings.Join(diff.Added, ", "))
	}
	if len(diff.Removed) > 0 {
		fmt.Printf("  삭제 %d건: %s\n", len(diff.Removed), strings.Join(diff.Removed, ", "))
	}
	if len(diff.Revised) > 0 {
		// Called out as work, not as news. An assessment made against the old
		// text settles nothing against the new one, so these went back to
		// unknown the moment the pack changed.
		fmt.Printf("  개정 %d건 — 기존 판정이 무효가 되어 재평가가 필요합니다: %s\n",
			len(diff.Revised), strings.Join(diff.Revised, ", "))
	}
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
			// The prior result is named. "needs re-assessment" alone cannot be
			// told apart from a criterion nobody ever looked at, and the two
			// call for different amounts of worry.
			stale = fmt.Sprintf(" (기준 개정 — 이전 판정 %s, 재평가 필요)", view.PriorResult)
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

// standardsGate records which criteria a verification gate settles.
//
// The link is declared rather than inferred. Matching a gate to a criterion by
// kind alone would let any journey test settle every journey criterion — the
// same mistake as letting a build gate settle a journey one, moved up a level.
func standardsGate(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("standards gate", flag.ContinueOnError)
	checkType := set.String("type", "", "게이트의 check type")
	settles := set.String("settles", "", "이 게이트가 정산하는 기준 ID 목록 (쉼표 구분)")
	produces := set.String("produces", "", "이 게이트가 만들어 내는 근거 종류 (쉼표 구분)")
	if err := set.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*checkType) == "" {
		return errors.New("--type 이 필요합니다")
	}
	project, _, pack, err := standardsContext(ctx, s)
	if err != nil {
		return err
	}
	claim := store.GateClaim{CheckType: *checkType, Settles: splitList(*settles), Produces: splitList(*produces)}
	if err = s.SetGateClaim(ctx, project.ID, claim, pack); err != nil {
		return err
	}
	fmt.Printf("%s 게이트가 정산하는 기준: %s\n", claim.CheckType, strings.Join(claim.Settles, ", "))
	if len(claim.Produces) > 0 {
		fmt.Printf("추가로 만들어 내는 근거: %s\n", strings.Join(claim.Produces, ", "))
	}
	return nil
}

// standardsSettle reads the project's gate results and re-judges the criteria
// they claim.
//
// It is the only path by which a criterion can reach MET: the static read
// proves absences and cannot prove that anything works, so until something is
// actually run every criterion it cannot see stays unknown.
func standardsSettle(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("standards settle", flag.ContinueOnError)
	commit := set.String("commit", "", "근거로 기록할 커밋 (기본: 기본 브랜치의 HEAD)")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, profile, pack, err := standardsContext(ctx, s)
	if err != nil {
		return err
	}
	sha := *commit
	if sha == "" {
		if sha, err = gitops.HeadCommit(ctx, project.RepositoryPath, project.DefaultBranch); err != nil {
			return err
		}
	}
	goal, err := s.CurrentGoal(ctx, project.ID)
	if err != nil {
		return err
	}
	settled, err := observer.SettleFromGates(ctx, s, project.ID, goal.ID, sha, pack, profile, version)
	if err != nil {
		return err
	}
	if len(settled) == 0 {
		fmt.Println("게이트가 정산한 기준이 없습니다 — `goalforge standards gate --type T --settles ID` 로 연결하세요")
		return nil
	}
	ids := make([]string, 0, len(settled))
	for id := range settled {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("%s  %s\n", resultMark(settled[id]), id)
	}
	return nil
}

// standardsAutonomy approves the findings inside the operator's envelope.
//
// It approves work to *run* and nothing further. The merge boundary stays a
// person's: a supplier that could approve its own merges would be the only
// reviewer of its own work.
func standardsAutonomy(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("standards autonomy", flag.ContinueOnError)
	enable := set.Bool("enable", false, "이 실행에서 자동 승인을 켠다")
	allowStandards := set.String("standards", "", "자동 승인할 기준 ID 목록 (쉼표 구분)")
	allowScopes := set.String("scopes", "", "자동 승인할 변경 범위 목록 (쉼표 구분)")
	allStandards := set.Bool("all-standards", false, "모든 기준을 자동 승인 대상으로 한다")
	allScopes := set.Bool("all-scopes", false, "변경 범위 제한을 두지 않는다")
	autoMerge := set.Bool("merge", false, "검증이 끝난 작업의 병합 승인까지 자동으로 한다")
	maxTokens := set.Int64("max-tokens", 20000, "자동 승인할 작업 하나의 크기 한도 (0 이면 제한 없음)")
	dailyLimit := set.Int("daily-limit", 3, "하루에 자동 승인할 최대 건수 (0 이면 제한 없음)")
	full := set.Bool("full", false, "모든 기준·범위를 열고 병합 승인까지 자동으로 한다")
	save := set.Bool("save", false, "이 설정을 저장해 워커가 무인으로 적용하게 한다")
	show := set.Bool("show", false, "저장된 설정을 보여 준다")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *full {
		// One flag for "run the whole loop". Spelling it out as four is how an
		// operator ends up with three of them set and wonders why nothing
		// moves.
		*enable, *allStandards, *allScopes, *autoMerge = true, true, true, true
	}
	project, _, _, err := standardsContext(ctx, s)
	if err != nil {
		return err
	}
	goal, err := s.CurrentGoal(ctx, project.ID)
	if err != nil {
		return err
	}
	policy := observer.AutonomyPolicy{Enabled: *enable, AllowedStandards: splitList(*allowStandards),
		AllStandards: *allStandards, AllowedScopes: splitList(*allowScopes), AllScopes: *allScopes,
		MaxTokens: *maxTokens, DailyLimit: *dailyLimit, AutoMerge: *autoMerge}
	if *show || (!*enable && !*save) {
		// Reading with no flags shows what is saved rather than running with
		// the flag defaults. The defaults say "switched off", and a command
		// that reported that while a saved envelope was quietly running would
		// tell the operator the opposite of the truth.
		saved, loadErr := s.AutonomyConfigFor(ctx, project.ID)
		if errors.Is(loadErr, store.ErrNotFound) {
			fmt.Println("저장된 자동 승인 설정이 없습니다 — `--full --save` 로 켭니다")
			return nil
		}
		if loadErr != nil {
			return loadErr
		}
		printAutonomyConfig(saved)
		if *show {
			return nil
		}
		policy = observer.AutonomyPolicy{Enabled: saved.Enabled, AllowedStandards: saved.AllowedStandards,
			AllStandards: saved.AllStandards, AllowedScopes: saved.AllowedScopes, AllScopes: saved.AllScopes,
			MaxTokens: saved.MaxTokens, DailyLimit: saved.DailyLimit, AutoMerge: saved.AutoMerge}
	}
	if *save {
		if err = s.SaveAutonomyConfig(ctx, store.AutonomyConfig{ProjectID: project.ID, Enabled: policy.Enabled,
			AllStandards: policy.AllStandards, AllowedStandards: policy.AllowedStandards,
			AllScopes: policy.AllScopes, AllowedScopes: policy.AllowedScopes, MaxTokens: policy.MaxTokens,
			DailyLimit: policy.DailyLimit, AutoMerge: policy.AutoMerge}); err != nil {
			return err
		}
		fmt.Println("저장했습니다 — `goalforge worker` 가 무인으로 적용합니다")
	}
	execution, err := observer.AutoApprove(ctx, s, project.ID, goal.ID, policy)
	if err != nil {
		return err
	}
	printDecisions("실행", execution)
	if !policy.AutoMerge {
		fmt.Println("병합은 자동 승인 대상이 아닙니다 — `--merge` 또는 `--full` 로 켭니다")
		return nil
	}
	merges, err := observer.AutoApproveMerges(ctx, s, project.ID, goal.ID, policy)
	if err != nil {
		return err
	}
	printDecisions("병합", merges)
	if len(merges.Approved) > 0 {
		fmt.Println("`goalforge merge --work-item ID` 로 반영합니다 — 승인은 이미 되어 있습니다")
	}
	return nil
}

// printDecisions reports what one pass decided, including what it declined and
// why. A run that only printed its approvals would read as "nothing else was
// eligible" when the truth is usually "several things were and here is what
// stopped them".
func printDecisions(label string, decisions observer.AutoDecisions) {
	if len(decisions.Approved) > 0 {
		fmt.Printf("%s 자동 승인: %s\n", label, strings.Join(decisions.Approved, ", "))
	}
	ids := make([]string, 0, len(decisions.Refused))
	for id := range decisions.Refused {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("%s 보류 %s: %s\n", label, id, decisions.Refused[id])
	}
	if decisions.Detail != "" {
		fmt.Printf("%s: %s\n", label, decisions.Detail)
	}
	if len(decisions.Approved) == 0 && len(decisions.Refused) == 0 && decisions.Detail == "" {
		fmt.Printf("%s 자동 승인 대상이 없습니다\n", label)
	}
}

// printAutonomyConfig shows a saved envelope in the terms it was written in.
func printAutonomyConfig(config store.AutonomyConfig) {
	state := "꺼짐"
	if config.Enabled {
		state = "켜짐"
	}
	criteria := strings.Join(config.AllowedStandards, ", ")
	if config.AllStandards {
		criteria = "전체"
	}
	scopes := strings.Join(config.AllowedScopes, ", ")
	if config.AllScopes {
		scopes = "전체"
	}
	merge := "사람이 승인"
	if config.AutoMerge {
		merge = "자동 승인"
	}
	fmt.Printf("자동 승인 %s · 기준 %s · 범위 %s · 크기 한도 %d · 하루 %d건 · 병합 %s\n",
		state, dashIfBlank(criteria), dashIfBlank(scopes), config.MaxTokens, config.DailyLimit, merge)
}

func dashIfBlank(value string) string {
	if strings.TrimSpace(value) == "" {
		return "없음"
	}
	return value
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
