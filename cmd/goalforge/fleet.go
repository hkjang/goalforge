package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/patterns"
	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// standardsCompare shows one pack across every project pinned to it.
func standardsCompare(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("standards compare", flag.ContinueOnError)
	packRef := set.String("pack", "", "비교할 개발팩")
	if err := set.Parse(args); err != nil {
		return err
	}
	pack, err := packByRef(*packRef)
	if err != nil {
		return err
	}
	report, err := s.Fleet(ctx, pack, time.Now())
	if err != nil {
		return err
	}
	if len(report.Projects) == 0 {
		fmt.Printf("%s 를 적용한 프로젝트가 없습니다\n", pack.Ref())
		return nil
	}
	fmt.Printf("%s — 프로젝트 %d개: %s\n\n", pack.Ref(), len(report.Projects), strings.Join(report.Projects, ", "))
	// Worst first: the reader opened this to find where the fleet is weakest,
	// and a list in catalogue order makes them scan for it.
	sort.SliceStable(report.Criteria, func(i, j int) bool {
		return fleetGap(report.Criteria[i]) > fleetGap(report.Criteria[j])
	})
	fmt.Printf("%-10s %-9s %5s %5s %5s %5s %5s  %s\n", "기준", "등급", "적용", "충족", "미충족", "미확인", "제외", "제목")
	for _, criterion := range report.Criteria {
		fmt.Printf("%-10s %-9s %5d %5d %5d %5d %5d  %s\n", criterion.StandardID, criterion.Severity,
			criterion.Applicable, criterion.Met, criterion.Unmet, criterion.Unknown, criterion.Excepted,
			criterion.Title)
	}
	proposals, err := s.ProposePackChanges(ctx, pack, time.Now())
	if err != nil {
		return err
	}
	if len(proposals) == 0 {
		return nil
	}
	fmt.Printf("\n팩을 다시 볼 신호 %d건 — 이 항목들은 프로젝트가 아니라 기준 쪽이 문제일 수 있습니다\n", len(proposals))
	for _, proposal := range proposals {
		fmt.Printf("  %s %s\n     %s\n     → %s\n", proposal.StandardID, proposal.Title,
			proposal.Evidence, proposal.Change)
	}
	return nil
}

// fleetGap ranks a criterion by how much of the fleet it is not satisfied in.
func fleetGap(criterion store.CriterionAcross) int {
	weight := criterion.Unmet*3 + criterion.Unknown
	if criterion.Severity == standards.SeverityRequired {
		weight *= 2
	}
	return weight
}

// patternList shows the archive with the evidence behind each entry.
func patternList(ctx context.Context, s *store.Store) error {
	views, err := s.Patterns(ctx)
	if err != nil {
		return err
	}
	if len(views) == 0 {
		fmt.Println("보관된 패턴이 없습니다")
		return nil
	}
	for _, view := range views {
		mark := "[ ]"
		switch {
		case view.Status == patterns.StatusRetired:
			mark = "[x]"
		case view.Status == patterns.StatusApproved:
			mark = "[v]"
		case view.Evidence.Promotable():
			mark = "[+]"
		}
		fmt.Printf("%s %s  %s — %s\n", mark, view.ID, view.StandardID, view.Problem)
		fmt.Printf("      해법: %s\n", view.Approach)
		fmt.Printf("      %s\n", view.Evidence.Reason())
		if view.Status == patterns.StatusRetired {
			fmt.Printf("      철회: %s\n", view.RetiredReason)
		}
		if view.Evidence.Promotable() && view.Status == patterns.StatusCandidate {
			fmt.Printf("      → `goalforge pattern approve %s --decider 이름` 으로 권고할 수 있습니다\n", view.ID)
		}
		if view.Evidence.ShouldRetire() && view.Status != patterns.StatusRetired {
			fmt.Printf("      → `goalforge pattern retire %s --reason ...` 를 검토하세요\n", view.ID)
		}
	}
	return nil
}

// patternAdd files a fix in the archive.
func patternAdd(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("pattern add", flag.ContinueOnError)
	id := set.String("id", "", "패턴 ID")
	standardID := set.String("standard", "", "어떤 기준에 대한 것인지")
	problem := set.String("problem", "", "반복되는 문제")
	approach := set.String("approach", "", "검증된 해결 방식")
	source := set.String("source", "", "출처")
	appliesWhen := set.String("applies-when", "", "key=value,key=value 적용 조건")
	if err := set.Parse(args); err != nil {
		return err
	}
	pattern := patterns.Pattern{ID: *id, StandardID: strings.ToUpper(strings.TrimSpace(*standardID)),
		Problem: *problem, Approach: *approach, Source: *source, Status: patterns.StatusCandidate,
		AppliesWhen: map[string]string{}}
	for _, pair := range splitList(*appliesWhen) {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("%q 는 key=value 형식이 아닙니다", pair)
		}
		pattern.AppliesWhen[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if err := s.SavePattern(ctx, pattern); err != nil {
		return err
	}
	fmt.Printf("%s 를 후보로 보관했습니다 — 서로 다른 프로젝트 %d곳에서 통해야 권고할 수 있습니다\n",
		pattern.ID, patterns.MinimumProjects)
	return nil
}

// patternApply records one use and its outcome.
func patternApply(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("pattern apply", flag.ContinueOnError)
	id := set.String("id", "", "패턴 ID")
	outcome := set.String("outcome", "", "PASSED 또는 FAILED")
	workItem := set.String("work-item", "", "적용한 작업")
	detail := set.String("detail", "", "무슨 일이 있었는지")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if err = s.RecordPatternApplication(ctx, patterns.Application{PatternID: *id, ProjectID: project.ID,
		WorkItemID: *workItem, Outcome: strings.ToUpper(strings.TrimSpace(*outcome)), Detail: *detail}); err != nil {
		return err
	}
	view, err := s.PatternByID(ctx, *id)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", view.ID, view.Evidence.Reason())
	return nil
}

// patternDecide approves or retires a pattern.
func patternDecide(ctx context.Context, s *store.Store, id string, args []string, retire bool) error {
	set := flag.NewFlagSet("pattern decide", flag.ContinueOnError)
	decider := set.String("decider", "", "결정자")
	reason := set.String("reason", "", "철회 사유")
	if err := set.Parse(args); err != nil {
		return err
	}
	view, err := s.PatternByID(ctx, id)
	if err != nil {
		return err
	}
	pattern := view.Pattern
	if retire {
		pattern.Status, pattern.RetiredReason = patterns.StatusRetired, *reason
	} else {
		if !view.Evidence.Promotable() {
			// The bar is not paperwork. A pattern put forward on one project's
			// evidence is that project's habit spreading under a name that
			// implies it was checked.
			return errors.New("아직 권고할 수 없습니다: " + view.Evidence.Reason())
		}
		pattern.Status, pattern.Decider, pattern.DecidedAt = patterns.StatusApproved, *decider, time.Now().UTC()
	}
	if err = s.SavePattern(ctx, pattern); err != nil {
		return err
	}
	fmt.Printf("%s → %s\n", pattern.ID, pattern.Status)
	return nil
}
