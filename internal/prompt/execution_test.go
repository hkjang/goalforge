package prompt

import (
	"strings"
	"testing"
)

// Context is appended with its provenance and with the rules that make it
// usable: a session that treats a settled decision as a suggestion will
// re-litigate it, and one that cannot see past failures will repeat them.
func TestWithContext(t *testing.T) {
	base := "프로젝트 목표:\n무언가"
	sections := []ContextSection{
		{Heading: "이미 내려진 설계 결정", Items: []ContextLine{{Title: "세션 저장소", Body: "SQLite 에 보관", Source: "DEC-1", AsOf: "2026-09-01"}}},
		{Heading: "변경 제약", Items: nil},
		{Heading: "이 작업의 이전 실패", Items: []ContextLine{{Title: "coverage FAILED", Body: "71.4%", Source: "R1"}}},
	}
	rendered := WithContext(base, sections)
	for _, expected := range []string{"이미 내려진 설계 결정:", "- 세션 저장소 [DEC-1 2026-09-01]", "SQLite 에 보관",
		"이 작업의 이전 실패:", "위 맥락 사용 규칙:", "검증 명령과 기준은 완화하지 않는다"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("missing %q in:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "변경 제약:") {
		t.Fatal("an empty section must not produce a heading")
	}
	if got := WithContext(base, []ContextSection{{Heading: "빈 것", Items: nil}}); got != base {
		t.Fatalf("with nothing to add the prompt is unchanged, got:\n%s", got)
	}
}
