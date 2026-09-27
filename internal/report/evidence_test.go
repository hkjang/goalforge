package report

import (
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// A bundle that only keeps the good news describes a different project than
// the one that happened, so refused approvals and relaxed checks have to
// survive into the document.
func TestEvidenceHTMLKeepsTheUncomfortableRecord(t *testing.T) {
	bundle := store.EvidenceBundle{
		GeneratedAt: time.Now().UTC(),
		Project:     model.Project{Name: "checkout", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "claude"},
		Goal:        model.Goal{Title: "결제 안정화", Version: 2},
		Criteria: []store.CriterionStatus{
			{Type: "coverage", ExpectedValue: "85", ActualValue: "71.4", Status: "UNMET"},
			{Type: "build_passed", ExpectedValue: "true", ActualValue: "true", Status: "STALE", StaleReason: "verified code changed"},
		},
		Approvals: []store.EvidenceApproval{
			{Approval: store.Approval{ID: "APR-1", ActionType: "MERGE_BRANCH", Reason: "머지 요청", Status: "REJECTED"},
				RejectionCategory: "insufficient_evidence", RejectionNote: "커버리지 근거 없음"},
		},
		Relaxations: []store.VerificationRelaxation{
			{Kind: "threshold_lowered", Detail: "coverage 기준값이 낮아졌습니다", Before: "85", After: "70"},
		},
	}
	var out strings.Builder
	if err := EvidenceHTML(&out, bundle); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	for _, expected := range []string{"결제 안정화", "기준 미달", "재검증 필요", "verified code changed",
		"REJECTED", "insufficient_evidence", "커버리지 근거 없음", "검증 기준 변경", "threshold_lowered"} {
		if !strings.Contains(page, expected) {
			t.Errorf("evidence must keep %q", expected)
		}
	}
	// Self-contained: the document has to keep working with no network.
	for _, external := range []string{"http://", "https://", "<script"} {
		if strings.Contains(page, external) {
			t.Errorf("the bundle must not depend on %q", external)
		}
	}
}

// Untrusted repository text goes into the document, so it has to be escaped.
func TestEvidenceHTMLEscapesContent(t *testing.T) {
	bundle := store.EvidenceBundle{
		Project: model.Project{Name: `<img src=x onerror="alert(1)">`},
		Goal:    model.Goal{Title: "t"},
		WorkItems: []store.EvidenceWorkItem{
			{Item: model.WorkItem{ID: "W1", Title: `</td><script>alert(2)</script>`, Status: "DONE"}},
		},
	}
	var out strings.Builder
	if err := EvidenceHTML(&out, bundle); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	if strings.Contains(page, "<script>alert(2)") || strings.Contains(page, `onerror="alert(1)"`) {
		t.Fatalf("unescaped content in the document:\n%s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Fatal("expected the injected markup to appear escaped")
	}
}
