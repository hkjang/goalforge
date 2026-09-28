package policy

import "testing"

// The bug this replaces: the refusal message concatenated a fixed particle, so
// every operation whose name ends in a vowel read "승인 는".
func TestParticlesAgreeWithTheFinalSound(t *testing.T) {
	for _, tc := range []struct{ word, topic, object string }{
		{"승인", "승인은", "승인을"},
		{"승인 반려", "승인 반려는", "승인 반려를"},
		{"목표 변경", "목표 변경은", "목표 변경을"},
		{"기본 브랜치 병합", "기본 브랜치 병합은", "기본 브랜치 병합을"},
		{"원격 게시", "원격 게시는", "원격 게시를"},
		{"작업 인계", "작업 인계는", "작업 인계를"},
		{"상태 복원", "상태 복원은", "상태 복원을"},
		{"저장소 마이그레이션", "저장소 마이그레이션은", "저장소 마이그레이션을"},
	} {
		if got := Topic(tc.word); got != tc.topic {
			t.Errorf("Topic(%q)=%q want %q", tc.word, got, tc.topic)
		}
		if got := Object(tc.word); got != tc.object {
			t.Errorf("Object(%q)=%q want %q", tc.word, got, tc.object)
		}
	}
}

// Trailing punctuation must not decide the particle; the sound before it does.
func TestTrailingPunctuationDoesNotDecide(t *testing.T) {
	if got := Object("원격 게시!"); got != "원격 게시!를" {
		t.Errorf("got %q", got)
	}
}

func TestDigitsAgreeWithHowTheyAreRead(t *testing.T) {
	// 1 is 일 (final consonant), 2 is 이 (none).
	if got := Topic("APR-1"); got != "APR-1은" {
		t.Errorf("got %q", got)
	}
	if got := Topic("APR-2"); got != "APR-2는" {
		t.Errorf("got %q", got)
	}
}

func TestEmptyWordDoesNotPanic(t *testing.T) {
	if got := Topic(""); got != "는" {
		t.Errorf("got %q", got)
	}
}
