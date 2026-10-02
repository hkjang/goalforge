package standards

import (
	"testing"
	"time"
)

func exceptedPack() Pack {
	return Pack{ID: "test", Version: "1", Title: "t",
		Standards: []Standard{{ID: "T-001", Revision: 1, Title: "기준", Category: "quality",
			Severity: SeverityRequired, Intent: "무언가를 보장한다",
			EvidenceRequired: []string{"test_result"},
			Checks:           []Check{{Type: "test", Assertion: "확인한다"}}}}}
}

// The validator insists a required criterion's exception carries a review date
// or a review condition, and says why: "without one the exception outlives the
// circumstances that justified it and nobody notices."
//
// The dated branch delivers that — Expired() puts the criterion back in force.
// The condition branch delivered nothing: a prose condition nothing evaluates
// and nothing surfaces is a permanent exception with a sentence attached, which
// is the outcome the rule exists to prevent.
func TestAConditionalExceptionStopsBeingTakenOnTrust(t *testing.T) {
	decided := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exception := Exception{StandardID: "T-001", Reason: "내부 레지스트리 반입 전",
		Decider: "hkjang", ReviewWhen: "반입 완료 시", DecidedAt: decided}
	profile := Profile{ProjectID: "PRJ-1", PackRef: exceptedPack().Ref(),
		Exceptions: []Exception{exception}}
	if err := profile.Validate(exceptedPack()); err != nil {
		t.Fatal(err)
	}
	// Inside the window it is taken on trust: somebody decided this recently
	// and the condition is the reason.
	fresh := decided.Add(30 * 24 * time.Hour)
	if _, live := profile.ExceptionFor("T-001", fresh); !live {
		t.Fatal("a recent decision stands")
	}
	if exception.DueForReview(fresh) {
		t.Fatal("30일은 아직 다시 볼 때가 아닙니다")
	}
	// Past it, the condition has had long enough that nobody can claim it is
	// still the same situation. The criterion comes back into force.
	stale := decided.Add(400 * 24 * time.Hour)
	if !exception.DueForReview(stale) {
		t.Fatal("조건부 예외가 영구 예외가 되었습니다")
	}
	if _, live := profile.ExceptionFor("T-001", stale); live {
		t.Fatal("an exception nobody revisited is not an exception")
	}
	inForce := InForce(exceptedPack(), profile, stale)
	if len(inForce) != 1 {
		t.Fatalf("the criterion is back in force: %+v", inForce)
	}
}

// An exception carrying both a date and a condition is governed by the date.
// The window exists because a condition cannot be evaluated; when somebody
// chose a date there is nothing to substitute for, and ageing it out at the
// default window would retire a decision earlier than the person who made it
// said to.
func TestAnExceptionWithBothIsGovernedByItsDate(t *testing.T) {
	decided := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exception := Exception{StandardID: "T-001", Reason: "r", Decider: "d",
		ReviewWhen: "반입 완료 시", ReviewBy: decided.Add(600 * 24 * time.Hour), DecidedAt: decided}
	profile := Profile{ProjectID: "PRJ-1", PackRef: exceptedPack().Ref(),
		Exceptions: []Exception{exception}}
	// Past the default window, before the chosen date: still live.
	beyondWindow := decided.Add(400 * 24 * time.Hour)
	if exception.DueForReview(beyondWindow) {
		t.Fatal("기본 창이 사람이 고른 날짜보다 먼저 예외를 끝내면 안 됩니다")
	}
	if _, live := profile.ExceptionFor("T-001", beyondWindow); !live {
		t.Fatal("the date somebody chose has not passed")
	}
	// Past the chosen date: expired, by its own date.
	afterDate := decided.Add(700 * 24 * time.Hour)
	if !exception.Expired(afterDate) {
		t.Fatal("the chosen date passed")
	}
	if _, live := profile.ExceptionFor("T-001", afterDate); live {
		t.Fatal("an exception past its own review date is not an exception")
	}
}

// A dated exception keeps its own date. The condition window must not shorten
// or extend a review date somebody chose deliberately.
func TestADatedExceptionUsesItsOwnDate(t *testing.T) {
	decided := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exception := Exception{StandardID: "T-001", Reason: "r", Decider: "d",
		ReviewBy: decided.Add(600 * 24 * time.Hour), DecidedAt: decided}
	// Past the condition window but before its own review date: still live.
	beyondWindow := decided.Add(400 * 24 * time.Hour)
	if exception.Expired(beyondWindow) {
		t.Fatal("its own date has not passed")
	}
	if exception.DueForReview(beyondWindow) {
		t.Fatal("a date somebody chose is not overridden by the default window")
	}
	// And a date that has passed expires, as it already did.
	if !exception.Expired(decided.Add(700 * 24 * time.Hour)) {
		t.Fatal("the chosen date passed")
	}
}

// An exception with neither — only allowed on a non-required criterion — is
// not aged out. Nobody promised to revisit it, and expiring it would make
// every advisory exception a recurring chore.
func TestAnUnconditionalExceptionOnAnAdvisoryCriterionIsNotAgedOut(t *testing.T) {
	decided := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exception := Exception{StandardID: "T-001", Reason: "r", Decider: "d", DecidedAt: decided}
	if exception.DueForReview(decided.Add(2000 * 24 * time.Hour)) {
		t.Fatal("nothing was promised about revisiting this one")
	}
}

// An exception with no decision date cannot be aged, and treating "no date" as
// "infinitely old" would retire every exception written before the field was
// recorded.
func TestAnExceptionWithNoDecisionDateIsNotAgedOut(t *testing.T) {
	exception := Exception{StandardID: "T-001", Reason: "r", Decider: "d", ReviewWhen: "조건"}
	if exception.DueForReview(time.Now()) {
		t.Fatal("there is no date to measure age from")
	}
}

// A criterion that came back because its exception lapsed must say so. Falling
// back to "아직 확인하지 않았습니다" makes it indistinguishable from one nobody
// ever looked at, and the remedy is different: one needs assessing, the other
// needs somebody to decide whether the waiver still holds.
func TestACriterionThatCameBackSaysItsExceptionLapsed(t *testing.T) {
	decided := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	profile := Profile{ProjectID: "PRJ-1", PackRef: exceptedPack().Ref(),
		Exceptions: []Exception{{StandardID: "T-001", Reason: "내부 레지스트리 반입 전",
			Decider: "hkjang", ReviewWhen: "반입 완료 시", DecidedAt: decided}}}
	stale := decided.Add(400 * 24 * time.Hour)
	entries := Apply(exceptedPack(), profile, stale)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v", entries)
	}
	entry := entries[0]
	if entry.Excepted {
		t.Fatal("it is not excepted any more")
	}
	lapsed, ok := entry.LapsedException()
	if !ok {
		t.Fatal("the criterion came back because a decision aged out, and that is the reason")
	}
	if lapsed.ReviewWhen != "반입 완료 시" {
		t.Fatalf("the condition somebody has to check travels with it: %+v", lapsed)
	}
	if lapsed.Decider != "hkjang" {
		t.Fatalf("and who decided it: %+v", lapsed)
	}
	// A live exception is not lapsed.
	fresh := Apply(exceptedPack(), profile, decided.Add(30*24*time.Hour))[0]
	if _, stillOk := fresh.LapsedException(); stillOk {
		t.Fatal("it is still being taken on trust")
	}
	if !fresh.Excepted {
		t.Fatal("fresh=excepted")
	}
	// And a criterion nobody ever excepted has nothing lapsed.
	bare := Apply(exceptedPack(), Profile{ProjectID: "PRJ-1", PackRef: exceptedPack().Ref()}, stale)[0]
	if _, bareOk := bare.LapsedException(); bareOk {
		t.Fatal("nobody excepted it")
	}
}
