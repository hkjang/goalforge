package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/goalforge/goalforge/internal/browser"
	"github.com/goalforge/goalforge/internal/capture"
	"github.com/goalforge/goalforge/internal/gitops"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// loadManifest reads the page manifest from disk.
func loadManifest(path string) (capture.Manifest, error) {
	var manifest capture.Manifest
	if path == "" {
		path = "goalforge.pages.json"
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return manifest, fmt.Errorf("페이지 매니페스트를 읽지 못했습니다 (%s): %w", path, err)
	}
	if err = json.Unmarshal(body, &manifest); err != nil {
		return manifest, fmt.Errorf("%s: %w", path, err)
	}
	return manifest, manifest.Validate()
}

func captureContext(ctx context.Context, s *store.Store, manifestPath string) (capture.Manifest, string, string, error) {
	manifest, err := loadManifest(manifestPath)
	if err != nil {
		return manifest, "", "", err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return manifest, "", "", err
	}
	head, err := gitops.HeadCommit(ctx, project.RepositoryPath, project.DefaultBranch)
	if err != nil {
		return manifest, "", "", err
	}
	return manifest, project.ID, head, nil
}

// captureStatus reports which screens the documentation depicts accurately.
func captureStatus(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("capture status", flag.ContinueOnError)
	manifestPath := set.String("manifest", "", "페이지 매니페스트 경로 (기본 goalforge.pages.json)")
	if err := set.Parse(args); err != nil {
		return err
	}
	manifest, projectID, head, err := captureContext(ctx, s, *manifestPath)
	if err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	report, err := capture.Status(ctx, s, project.RepositoryPath, projectID, head, manifest)
	if err != nil {
		return err
	}
	fmt.Printf("화면 %d개 — 최신 %d · 낡음 %d · 없음 %d\n\n",
		len(report.Statuses), report.Current, report.Stale, report.Missing)
	for _, status := range report.Statuses {
		fmt.Printf("%s  %s\n", captureMark(status.Status), status.Page.Label())
		if status.Reason != "" {
			fmt.Printf("     %s\n", status.Reason)
		}
	}
	return nil
}

func captureMark(status string) string {
	switch status {
	case capture.StatusCurrent:
		return "[v]"
	case capture.StatusStale:
		return "[~]"
	}
	return "[ ]"
}

// captureRun takes the screenshots that need taking.
func captureRun(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("capture run", flag.ContinueOnError)
	manifestPath := set.String("manifest", "", "페이지 매니페스트 경로")
	service := set.String("service", "", "playwright-player 주소")
	timeout := set.Duration("timeout", 10*time.Minute, "한 화면의 제한 시간")
	gate := set.Bool("gate", false, "필수 화면이 최신이 아니면 0이 아닌 코드로 끝낸다")
	if err := set.Parse(args); err != nil {
		return err
	}
	base := browserBaseURL(*service)
	if base == "" {
		return fmt.Errorf("%s 를 설정하거나 --service 로 playwright-player 주소를 주세요", browserServiceEnv)
	}
	manifest, projectID, head, err := captureContext(ctx, s, *manifestPath)
	if err != nil {
		return err
	}
	project, err := currentProject(ctx, s)
	if err != nil {
		return err
	}
	client := browser.Client{BaseURL: base, Timeout: *timeout}
	outcome, err := capture.Run(ctx, s, client, project.RepositoryPath, projectID, head, version, manifest, nil)
	if err != nil {
		return err
	}
	fmt.Printf("%s (커밋 %s)\n", outcome.Summary(), shortSHA(head))
	for _, label := range outcome.Captured {
		fmt.Printf("찍음 %s\n", label)
	}
	labels := make([]string, 0, len(outcome.Failed))
	for label := range outcome.Failed {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		fmt.Printf("실패 %s: %s\n", label, outcome.Failed[label])
	}
	if !*gate {
		return nil
	}
	report, err := capture.Status(ctx, s, project.RepositoryPath, projectID, head, manifest)
	if err != nil {
		return err
	}
	if short := report.Shortfall(); len(short) > 0 {
		// Non-zero so this can be a gate. A capture pass that reported its
		// failures and exited zero would let a release go out with a guide
		// showing screens that no longer exist.
		return errors.New("최신이 아닌 필수 화면: " + joinList(short))
	}
	fmt.Println("필수 화면이 모두 최신입니다")
	return nil
}

func joinList(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += ", "
		}
		out += value
	}
	return out
}
