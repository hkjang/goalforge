package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/observer"
)

// A sweep where nothing happened prints nothing. The cadence is every quarter
// of an hour, and a loop that says "nothing changed" ninety-six times a day
// trains the operator to scroll past the one line that was not that.
func TestAQuietSweepPrintsNothing(t *testing.T) {
	out, errs := sweepReport(observer.TickResult{Projects: []observer.ProjectTick{
		{ProjectName: "alpha", Note: "오늘의 발견 예산을 다 썼습니다"},
		{ProjectName: "beta"},
	}})
	if len(out) != 0 || len(errs) != 0 {
		t.Fatalf("out=%q errs=%q", out, errs)
	}
}

// When the sweep does have something to say, the projects that did nothing say
// why. The reason is computed on every tick and, until it was printed, the
// operator's only way to find out why a project had been idle for a week was to
// go and run the command by hand — which is the one thing an unattended loop
// exists to remove.
func TestASweepThatSpeaksSaysWhyTheQuietProjectsWereQuiet(t *testing.T) {
	out, errs := sweepReport(observer.TickResult{Projects: []observer.ProjectTick{
		{ProjectName: "alpha", Ran: true, Decision: observer.Decision{Trigger: "HEAD_CHANGED"}, Filed: []string{"W-1"}},
		{ProjectName: "beta", Note: "목표가 아직 없습니다"},
		{ProjectName: "gamma", Err: errors.New("저장소를 읽지 못했습니다")},
	}})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "alpha") {
		t.Fatalf("out=%q", out)
	}
	if !strings.Contains(joined, "beta") || !strings.Contains(joined, "목표가 아직 없습니다") {
		t.Fatalf("a quiet project's reason must reach the log: out=%q", out)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "gamma") {
		t.Fatalf("errs=%q", errs)
	}
	// A failure is not a note. They stay on different streams so a broken
	// project cannot hide among the quiet ones.
	if strings.Contains(joined, "gamma") {
		t.Fatalf("a failure must not be reported as a note: out=%q", out)
	}
}
