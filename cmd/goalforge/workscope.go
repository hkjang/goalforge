package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// workScope corrects a work item's change scope, or lists the items whose
// scope cannot match anything.
//
// It exists because an item whose scope is not a path list can never run —
// every file the session writes is reported as out of scope — and there was no
// way to fix one. The only remedy was to discard it and write it again, which
// loses everything else recorded against it.
func workScope(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("work scope", flag.ContinueOnError)
	item := set.String("item", "", "고칠 작업 항목 ID")
	to := set.String("set", "", "새 변경 범위: 경로나 glob 의 쉼표 구분 목록")
	list := set.Bool("list-unusable", false, "범위 때문에 실행될 수 없는 작업을 보여 준다")
	if err := set.Parse(args); err != nil {
		return err
	}
	goal, err := activeGoal(ctx, s)
	if err != nil {
		return err
	}
	if *list || (*item == "" && *to == "") {
		stuck, listErr := s.UnusableScopeItems(ctx, goal.ID)
		if listErr != nil {
			return listErr
		}
		if len(stuck) == 0 {
			fmt.Println("모든 작업의 변경 범위가 경로 목록입니다")
			return nil
		}
		fmt.Printf("변경 범위가 경로 목록이 아니어서 실행될 수 없는 작업 %d건:\n", len(stuck))
		for _, entry := range stuck {
			fmt.Printf("  %-18s %-10s %s\n", entry.ID, entry.Status, entry.Title)
			fmt.Printf("    현재 범위: %s\n", truncate(entry.ChangeScope, 100))
		}
		fmt.Println("\n`goalforge work scope --item ID --set internal/server/handler.go` 로 고칩니다")
		return nil
	}
	if strings.TrimSpace(*item) == "" || strings.TrimSpace(*to) == "" {
		return errors.New("--item 과 --set 이 함께 필요합니다 (--list-unusable 로 목록을 봅니다)")
	}
	updated, err := s.SetWorkItemScope(ctx, goal.ID, *item, *to)
	if err != nil {
		return err
	}
	fmt.Printf("%s 의 변경 범위: %s\n", updated.ID, updated.ChangeScope)
	return nil
}

// truncate keeps a listing readable when the value is a paragraph.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// unblockProject returns a blocked project to READY.
//
// It exists because a project blocked by a policy violation had no way back:
// the only transition out of BLOCKED was the checkpoint resume, and a run
// stopped by a violation never checkpointed, so the resume failed looking for
// one and the project stayed blocked after the cause was fixed.
func unblockProject(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("unblock", flag.ContinueOnError)
	reason := set.String("reason", "", "무엇을 고쳤는지")
	if err := set.Parse(args); err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	if err = s.UnblockProject(ctx, project.ID, *reason); err != nil {
		return err
	}
	fmt.Printf("%s 를 다시 실행할 수 있습니다\n", project.Name)
	// Named here because a project unblocked while the thing that blocked it is
	// still there will block again on the next run, and the operator will read
	// that as the unblock not having worked.
	stuck, listErr := func() ([]model.WorkItem, error) {
		goal, goalErr := activeGoal(ctx, s)
		if goalErr != nil {
			return nil, goalErr
		}
		return s.UnusableScopeItems(ctx, goal.ID)
	}()
	if listErr == nil && len(stuck) > 0 {
		fmt.Printf("주의: 변경 범위가 경로 목록이 아닌 작업이 %d건 남아 있습니다 — 실행하면 다시 막힙니다\n", len(stuck))
		fmt.Println("`goalforge work scope --list-unusable` 로 확인하세요")
	}
	return nil
}
