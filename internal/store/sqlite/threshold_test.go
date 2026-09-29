package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func seedEvidence(t *testing.T, ctx context.Context, s *Store, goalID, checkType, status, value string) {
	t.Helper()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,check_type,status,actual_value,required,created_at,evidence_kind) VALUES(?,?,?,?,1,?,?)`,
		goalID, checkType, status, value, "2026-09-29T00:00:00Z", "performance"); err != nil {
		t.Fatal(err)
	}
}

// The bug this file exists for. A latency budget is a ceiling: measuring 5000ms
// against a 200ms target is a failure by a factor of twenty-five. Judging every
// number with ">=" reads that as met, so a goal whose whole point was to be
// fast reports complete at its worst.
func TestACeilingIsNotJudgedAsAFloor(t *testing.T) {
	for _, tc := range []struct {
		expected, actual string
		want             bool
	}{
		{"<=200", "150", true},
		{"<=200", "200", true},
		{"<=200", "5000", false},
		{"<200", "200", false},
		{">=99.9", "99.95", true},
		{">=99.9", "98", false},
		{">99", "99", false},
		{"=0", "0", true},
		{"=0", "3", false},
		{"!=0", "3", true},
		{"!=0", "0", false},
	} {
		if got := CriterionMet(tc.expected, tc.actual); got != tc.want {
			t.Fatalf("expected=%q actual=%q met=%v want %v", tc.expected, tc.actual, got, tc.want)
		}
	}
}

// Korean and symbolic spellings mean the same thing. A person writing a goal
// should not have to learn which of them the parser happens to accept.
func TestComparatorSpellingsAgree(t *testing.T) {
	for _, expected := range []string{"<=200", "≤200", "max 200", "at_most 200", "최대 200"} {
		if !CriterionMet(expected, "150") || CriterionMet(expected, "5000") {
			t.Fatalf("%q must be a ceiling", expected)
		}
	}
	for _, expected := range []string{">=1000", "≥1000", "min 1000", "at_least 1000", "최소 1000"} {
		if !CriterionMet(expected, "1200") || CriterionMet(expected, "900") {
			t.Fatalf("%q must be a floor", expected)
		}
	}
}

// A bare number keeps meaning what it always meant. Goals already recorded were
// written under the ">=" rule, and silently flipping them would change the
// verdict on work already judged.
func TestABareNumberStillMeansAtLeast(t *testing.T) {
	if !CriterionMet("1000", "1200") || CriterionMet("1000", "900") {
		t.Fatal("a bare number must keep the floor meaning existing goals were written under")
	}
	if !CriterionMet("true", "true") || CriterionMet("true", "false") {
		t.Fatal("non-numeric expectations are equality")
	}
}

// Units are compared in the same unit or not at all. "200ms" against "3s" is a
// failure; comparing the bare numbers would call it a pass by nine-fold.
func TestUnitsAreConvertedNotIgnored(t *testing.T) {
	if !CriterionMet("<=200ms", "0.15s") {
		t.Fatal("150ms is under a 200ms ceiling however it is spelled")
	}
	if CriterionMet("<=200ms", "3s") {
		t.Fatal("3s is nine times over a 200ms ceiling")
	}
	if !CriterionMet("<=2MB", "1500KB") || CriterionMet("<=2MB", "3000KB") {
		t.Fatal("byte units must convert too")
	}
	if !CriterionMet(">=99.9%", "99.95%") {
		t.Fatal("a percent on both sides is the same unit")
	}
}

// Units from different families cannot be compared at all, and a judge that
// guesses is worse than one that refuses: it would settle a latency target with
// a memory measurement.
func TestIncomparableUnitsAreRefused(t *testing.T) {
	// The pairing matters. "<=200ms" against "150MB" comes out false either
	// way, because megabytes are a large number — a test built on it would pass
	// with the unit check deleted. "<=2MB" against "1s" is the case that turns:
	// ignoring units compares 1000 with 2097152 and calls a one-second
	// measurement a two-megabyte budget met.
	met, reason := JudgeCriterion("<=2MB", "1s")
	if met {
		t.Fatal("seconds do not satisfy a megabyte budget")
	}
	if reason == "" || !strings.Contains(reason, "단위") {
		t.Fatalf("the refusal must name the unit mismatch: %q", reason)
	}
	if met, _ := JudgeCriterion("<=200ms", "150MB"); met {
		t.Fatal("milliseconds and megabytes are not comparable")
	}
	// An unrecognised unit is still a unit and only compares with itself.
	if met, _ := JudgeCriterion(">=1000rps", "2000qps"); met {
		t.Fatal("rps and qps are different names and must not be assumed equal")
	}
	if met, _ := JudgeCriterion(">=1000rps", "2000rps"); !met {
		t.Fatal("the same unit on both sides compares normally")
	}
}

// The judgement is described in the words the goal was written in, so a report
// says "200ms 이하" rather than leaving the reader to infer the direction.
func TestExpectationDescribesItself(t *testing.T) {
	for expected, want := range map[string]string{
		"<=200ms": "200ms 이하", ">=1000": "1000 이상", "=0": "0", "true": "true",
	} {
		if got := ParseExpectation(expected).Describe(); got != want {
			t.Fatalf("%q describes as %q want %q", expected, got, want)
		}
	}
}

// A report that says only "UNMET" makes the reader open the run log to learn
// whether the number missed by a hair or by twentyfold, and in which direction.
func TestUnmetCriterionSaysWhatItWanted(t *testing.T) {
	ctx, s, project, goal := boardFixture(t)
	_ = project
	criterion := model.Criterion{Type: "p95_latency_ms", ExpectedValue: "<=200ms"}
	seedEvidence(t, ctx, s, goal.ID, "p95_latency_ms", "PASSED", "5000ms")
	entry, err := s.criterionStatus(ctx, goal.ID, criterion)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Satisfied {
		t.Fatal("5000ms does not satisfy a 200ms ceiling")
	}
	for _, want := range []string{"200ms 이하", "5000ms"} {
		if !strings.Contains(entry.Shortfall, want) {
			t.Fatalf("the shortfall must name %q: %q", want, entry.Shortfall)
		}
	}
}

// When the gate itself did not pass there is no measurement, and reporting a
// shortfall against whatever it printed points at the wrong problem.
func TestAFailedGateReportsTheFailureNotAShortfall(t *testing.T) {
	ctx, s, _, goal := boardFixture(t)
	seedEvidence(t, ctx, s, goal.ID, "p95_latency_ms", "TIMEOUT", "")
	entry, err := s.criterionStatus(ctx, goal.ID, model.Criterion{Type: "p95_latency_ms", ExpectedValue: "<=200ms"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entry.Shortfall, "TIMEOUT") {
		t.Fatalf("shortfall=%q", entry.Shortfall)
	}
}
