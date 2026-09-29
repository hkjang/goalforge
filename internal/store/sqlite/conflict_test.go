package sqlite

import (
	"strings"
	"testing"
)

func contractWith(outcomes ...RequiredOutcome) GoalContract {
	return GoalContract{Outcomes: outcomes}
}

func outcome(key, metric, comparator, threshold string) RequiredOutcome {
	return RequiredOutcome{Key: key, Metric: metric, Comparator: comparator, Threshold: threshold,
		Method: "gate:load", Judge: "verification"}
}

// The boundary is where a conflict detector earns its keep. "200 이상" and
// "200 이하" is satisfiable — at exactly 200 — and reporting it would train
// people to ignore the detector. "200 초과" and "200 이하" is satisfiable by
// nothing at all, and staying quiet about it lets a goal ship that no
// measurement can ever meet.
func TestStrictAndInclusiveBoundsAreDistinguished(t *testing.T) {
	satisfiable := contractWith(outcome("a", "rps", ">=", "200"), outcome("b", "rps", "<=", "200"))
	if conflicts := satisfiable.Conflicts(); len(conflicts) != 0 {
		t.Fatalf("200 satisfies both: %+v", conflicts)
	}
	impossible := contractWith(outcome("a", "rps", ">", "200"), outcome("b", "rps", "<=", "200"))
	conflicts := impossible.Conflicts()
	if len(conflicts) != 1 {
		t.Fatalf("no value is both above 200 and at most 200: %+v", conflicts)
	}
	if conflicts[0].Detail == "" {
		t.Fatal("a conflict with no explanation cannot be acted on")
	}
	if both := contractWith(outcome("a", "rps", ">", "200"), outcome("b", "rps", "<", "200")).Conflicts(); len(both) != 1 {
		t.Fatalf("above and below the same point is impossible: %+v", both)
	}
}

// An exact requirement is a range of one. Comparing it only against other exact
// requirements misses the ordinary case: "오류 건수 = 0" alongside "오류 건수
// 1 이상" is a contract nothing can satisfy.
func TestExactValueConflictsWithARange(t *testing.T) {
	for _, tc := range []struct {
		comparator, threshold string
		conflict              bool
	}{
		{">=", "1", true},
		{">", "0", true},
		{"<=", "0", false},
		{">=", "0", false},
	} {
		contract := contractWith(outcome("a", "errors", "=", "0"), outcome("b", "errors", tc.comparator, tc.threshold))
		got := len(contract.Conflicts()) > 0
		if got != tc.conflict {
			t.Fatalf("errors=0 with %s%s: conflict=%v want %v", tc.comparator, tc.threshold, got, tc.conflict)
		}
	}
}

// Thresholds carry units, and comparing the bare numbers turns "1초 이상" and
// "200ms 이하" — a flat contradiction — into a pair that looks compatible
// because 1 is less than 200.
func TestUnitsAreHonouredWhenDetectingConflicts(t *testing.T) {
	contract := contractWith(outcome("a", "latency", ">=", "1s"), outcome("b", "latency", "<=", "200ms"))
	if conflicts := contract.Conflicts(); len(conflicts) != 1 {
		t.Fatalf("1s is five times a 200ms ceiling: %+v", conflicts)
	}
	compatible := contractWith(outcome("a", "latency", ">=", "50ms"), outcome("b", "latency", "<=", "0.2s"))
	if conflicts := compatible.Conflicts(); len(conflicts) != 0 {
		t.Fatalf("50ms to 200ms is a real range: %+v", conflicts)
	}
}

// Units from different families cannot be shown to conflict, and claiming one
// would be the detector guessing. Silence is the honest answer.
func TestIncomparableUnitsAreNotCalledAConflict(t *testing.T) {
	// The pair matters. "1s" against "200MB" comes out compatible either way,
	// because 200MB is a huge number and the interval still overlaps. "1s"
	// against "200B" is the case that turns: with the units ignored it reads
	// as 1000 against 200 and reports a conflict that does not exist.
	contract := contractWith(outcome("a", "budget", ">=", "1s"), outcome("b", "budget", "<=", "200B"))
	if conflicts := contract.Conflicts(); len(conflicts) != 0 {
		t.Fatalf("seconds and bytes cannot be shown to contradict: %+v", conflicts)
	}
	if conflicts := contractWith(outcome("a", "budget", ">=", "1s"), outcome("b", "budget", "<=", "200MB")).Conflicts(); len(conflicts) != 0 {
		t.Fatalf("seconds and megabytes either: %+v", conflicts)
	}
}

// A threshold written without a unit is in whatever unit the metric is
// measured in. Refusing to compare it with a spelled-out unit would silence
// the detector on the most ordinary way a contract is written.
func TestABareThresholdComparesWithAUnitOne(t *testing.T) {
	conflicts := contractWith(outcome("a", "latency", ">=", "500"), outcome("b", "latency", "<=", "200ms")).Conflicts()
	if len(conflicts) != 1 {
		t.Fatalf("500 against a 200ms ceiling is a conflict: %+v", conflicts)
	}
}

// The explanation says both sides in the words they were written in, so the
// reader does not have to open the contract to see which two clauses collide.
func TestConflictDetailNamesBothSides(t *testing.T) {
	conflicts := contractWith(outcome("a", "latency", ">", "500ms"), outcome("b", "latency", "<=", "200ms")).Conflicts()
	if len(conflicts) != 1 {
		t.Fatalf("conflicts=%+v", conflicts)
	}
	for _, want := range []string{"500ms", "200ms", "latency"} {
		if !strings.Contains(conflicts[0].Detail, want) {
			t.Fatalf("the detail must name %q: %q", want, conflicts[0].Detail)
		}
	}
}
