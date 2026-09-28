package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/audit"
	"github.com/goalforge/goalforge/internal/model"
)

// unkeyedLinkForTest reproduces what someone without the chain key could
// compute, which is what the keyed guarantee is measured against.
func unkeyedLinkForTest(previous, payloadDigest, kind, recordID, recordedAt string) string {
	saved := os.Getenv(audit.EnvChainKey)
	os.Unsetenv(audit.EnvChainKey)
	defer os.Setenv(audit.EnvChainKey, saved)
	return audit.LinkDigest(previous, payloadDigest, kind, recordID, recordedAt)
}

func integrityFixture(t *testing.T) (context.Context, *Store, model.Goal) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p := model.Project{ID: "PRJ-1", Name: "demo", RepositoryPath: "/repo", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	goal, err := s.SetGoal(ctx, p.ID, "Goal", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, s, goal
}

func mustVerify(t *testing.T, ctx context.Context, s *Store) IntegrityReport {
	t.Helper()
	report, err := s.VerifyIntegrity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func hasFinding(report IntegrityReport, kind string) bool {
	for _, finding := range report.Findings {
		if finding.Kind == kind {
			return true
		}
	}
	return false
}

// Evidence written through GoalForge verifies clean. Anything else in this
// file would be meaningless if the normal path did not.
func TestEvidenceWrittenNormallyVerifies(t *testing.T) {
	ctx, s, goal := integrityFixture(t)
	if err := s.RecordVerification(ctx, goal.ID, "build_passed", "PASSED", "true", "ok"); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !report.Intact() {
		t.Fatalf("a clean history must verify: %+v", report.Findings)
	}
	if report.Entries == 0 {
		t.Fatal("the chain must actually have recorded something")
	}
}

// The hole this closes: a row written straight into the database. The session
// GoalForge orchestrates has write access to the same file, so "evidence" that
// nothing can distinguish from an INSERT is not evidence.
func TestEvidenceInsertedBehindGoalForgeIsDetected(t *testing.T) {
	ctx, s, goal := integrityFixture(t)
	if err := s.RecordVerification(ctx, goal.ID, "build_passed", "FAILED", "false", "broken"); err != nil {
		t.Fatal(err)
	}
	// Exactly what a session — or a person in a hurry — would do to make a
	// goal look complete.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,check_type,status,actual_value,required,created_at) VALUES(?,'build_passed','PASSED','true',1,'now')`, goal.ID); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !hasFinding(report, IntegrityUnchained) {
		t.Fatalf("a row written behind GoalForge must be detected: %+v", report.Findings)
	}
}

// Editing a recorded failure into a pass must be detectable, not just adding
// new rows.
func TestEvidenceEditedInPlaceIsDetected(t *testing.T) {
	ctx, s, goal := integrityFixture(t)
	if err := s.RecordVerification(ctx, goal.ID, "build_passed", "FAILED", "false", "broken"); err != nil {
		t.Fatal(err)
	}
	if mustVerify(t, ctx, s).Intact() != true {
		t.Fatal("precondition: the history starts clean")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE verification_results SET status='PASSED',actual_value='true'`); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !hasFinding(report, IntegrityRecordEdited) {
		t.Fatalf("an edited record must be detected: %+v", report.Findings)
	}
}

// Deleting inconvenient evidence must be as detectable as editing it.
func TestDeletedEvidenceIsDetected(t *testing.T) {
	ctx, s, goal := integrityFixture(t)
	if err := s.RecordVerification(ctx, goal.ID, "build_passed", "FAILED", "false", "broken"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM verification_results`); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !hasFinding(report, IntegrityRecordGone) {
		t.Fatalf("deleted evidence must be detected: %+v", report.Findings)
	}
}

// Rewriting the ledger itself must break the chain, or the ledger would only
// protect records from being edited and not itself.
func TestRewritingTheLedgerBreaksTheChain(t *testing.T) {
	ctx, s, goal := integrityFixture(t)
	for i := 0; i < 3; i++ {
		if err := s.RecordVerification(ctx, goal.ID, "build_passed", "FAILED", "false", "broken"); err != nil {
			t.Fatal(err)
		}
	}
	// Remove the middle link, as someone would to hide one result.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM audit_chain WHERE seq=(SELECT MIN(seq)+1 FROM audit_chain)`); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !hasFinding(report, IntegrityChainBroken) {
		t.Fatalf("removing a link must break the chain: %+v", report.Findings)
	}
}

// An approval moving from pending to approved is a legitimate change, so the
// chain must follow its transitions rather than flagging every decision.
func TestApprovalTransitionsVerifyCleanly(t *testing.T) {
	ctx, s, _ := integrityFixture(t)
	approval, err := s.RequestApproval(ctx, "PRJ-1", ApprovalProtectedFiles, "need to touch config")
	if err != nil {
		t.Fatal(err)
	}
	if !mustVerify(t, ctx, s).Intact() {
		t.Fatal("a requested approval must verify")
	}
	if err = s.Approve(ctx, "PRJ-1", approval.ID); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !report.Intact() {
		t.Fatalf("approving is a legitimate transition: %+v", report.Findings)
	}
}

// Granting yourself an approval by editing the row is the attack this covers:
// the approval gate is what stands between a change and the default branch.
func TestApprovalFlippedInPlaceIsDetected(t *testing.T) {
	ctx, s, _ := integrityFixture(t)
	approval, err := s.RequestApproval(ctx, "PRJ-1", ApprovalProtectedFiles, "need to touch config")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE approvals SET status='APPROVED',approved_at=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339Nano), approval.ID); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !hasFinding(report, IntegrityRecordEdited) {
		t.Fatalf("self-granted approval must be detected: %+v", report.Findings)
	}
}

// An approval conjured directly into the table has no chain entry at all.
func TestApprovalInsertedBehindGoalForgeIsDetected(t *testing.T) {
	ctx, s, _ := integrityFixture(t)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO approvals(id,project_id,action_type,reason,status,requested_at) VALUES('APR-FAKE','PRJ-1',?,'r','APPROVED','now')`, ApprovalMergeBranch); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !hasFinding(report, IntegrityUnchained) {
		t.Fatalf("a conjured approval must be detected: %+v", report.Findings)
	}
}

// Without a key the chain can be recomputed by whoever edited the records, so
// the report has to say which guarantee is actually in force.
func TestReportSaysWhetherTheChainIsKeyed(t *testing.T) {
	ctx, s, goal := integrityFixture(t)
	if err := s.RecordVerification(ctx, goal.ID, "build_passed", "PASSED", "true", "ok"); err != nil {
		t.Fatal(err)
	}
	if mustVerify(t, ctx, s).Keyed {
		t.Fatal("no key is configured here")
	}
}

// The guarantee a key buys: someone who edits a record and then recomputes the
// chain to cover their tracks still fails, because they cannot produce the
// MACs. Without this the chain only catches people who do not think to relink.
func TestKeyedChainSurvivesARecomputedLedger(t *testing.T) {
	t.Setenv("GOALFORGE_AUDIT_KEY", "chain-secret")
	ctx, s, goal := integrityFixture(t)
	if err := s.RecordVerification(ctx, goal.ID, "build_passed", "FAILED", "false", "broken"); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !report.Intact() || !report.Keyed {
		t.Fatalf("precondition: a keyed chain starts clean: %+v", report)
	}
	// Edit the record, then relink the ledger the way an attacker without the
	// key would: recompute the digests with a plain hash.
	if _, err := s.db.ExecContext(ctx, `UPDATE verification_results SET status='PASSED',actual_value='true'`); err != nil {
		t.Fatal(err)
	}
	var goalID, runID, checkType, status, actualValue, evidenceKind, createdAt, output string
	var required int
	if err := s.db.QueryRowContext(ctx, `SELECT goal_id,COALESCE(run_id,''),check_type,status,actual_value,COALESCE(evidence_kind,''),created_at,required,COALESCE(output,'') FROM verification_results`).
		Scan(&goalID, &runID, &checkType, &status, &actualValue, &evidenceKind, &createdAt, &required, &output); err != nil {
		t.Fatal(err)
	}
	forged := evidenceDigest(goalID, runID, checkType, status, actualValue, evidenceKind, createdAt, output, required)
	// Relink using the unkeyed digest, which is the best they can do.
	t.Setenv("GOALFORGE_AUDIT_KEY", "")
	var seq int64
	var kind, recordID, prevDigest, recordedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT seq,kind,record_id,prev_digest,recorded_at FROM audit_chain ORDER BY seq LIMIT 1`).
		Scan(&seq, &kind, &recordID, &prevDigest, &recordedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE audit_chain SET payload_digest=?,digest=? WHERE seq=?`,
		forged, unkeyedLinkForTest(prevDigest, forged, kind, recordID, recordedAt), seq); err != nil {
		t.Fatal(err)
	}
	// Back under the operator's key, the forgery does not hold up.
	t.Setenv("GOALFORGE_AUDIT_KEY", "chain-secret")
	report = mustVerify(t, ctx, s)
	if !hasFinding(report, IntegrityChainBroken) {
		t.Fatalf("a ledger relinked without the key must not verify: %+v", report.Findings)
	}
}

// The output is the text a person reads when deciding whether to trust a
// result: which test failed, what the compiler said, which line. It was not
// covered, so a failure's reason could be rewritten while the check answered
// that the records were exactly as GoalForge wrote them.
func TestRewritingAGateOutputIsDetected(t *testing.T) {
	ctx, s, goal := integrityFixture(t)
	if err := s.RecordVerification(ctx, goal.ID, "build_passed", "FAILED", "false",
		"undefined: criticalFunction — 핵심 기능이 구현되지 않음"); err != nil {
		t.Fatal(err)
	}
	if !mustVerify(t, ctx, s).Intact() {
		t.Fatal("precondition: the history starts clean")
	}
	// Status and value untouched; only the reason is rewritten.
	if _, err := s.db.ExecContext(ctx, `UPDATE verification_results SET output='사소한 경고'`); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !hasFinding(report, IntegrityRecordEdited) {
		t.Fatalf("rewriting why a gate failed must be detected: %+v", report.Findings)
	}
}

// Evidence recorded before the output was covered still verifies — reporting
// every one of those as edited would be crying wolf over a change GoalForge
// made to itself — but the report says how many are in that state rather than
// folding them into "intact".
func TestEvidencePredatingOutputCoverageIsCountedNotFlagged(t *testing.T) {
	ctx, s, goal := integrityFixture(t)
	if err := s.RecordVerification(ctx, goal.ID, "build_passed", "PASSED", "true", "ok"); err != nil {
		t.Fatal(err)
	}
	// Rewrite the chain entry to the digest as it was computed before the
	// output was covered, which is what an older install holds.
	var goalID, runID, checkType, status, actualValue, evidenceKind, createdAt string
	var required int
	if err := s.db.QueryRowContext(ctx, `SELECT goal_id,COALESCE(run_id,''),check_type,status,actual_value,COALESCE(evidence_kind,''),created_at,required FROM verification_results`).
		Scan(&goalID, &runID, &checkType, &status, &actualValue, &evidenceKind, &createdAt, &required); err != nil {
		t.Fatal(err)
	}
	legacy := legacyEvidenceDigest(goalID, runID, checkType, status, actualValue, evidenceKind, createdAt, required)
	var seq int64
	var kind, recordID, prevDigest, recordedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT seq,kind,record_id,prev_digest,recorded_at FROM audit_chain ORDER BY seq LIMIT 1`).
		Scan(&seq, &kind, &recordID, &prevDigest, &recordedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE audit_chain SET payload_digest=?,digest=? WHERE seq=?`,
		legacy, unkeyedLinkForTest(prevDigest, legacy, kind, recordID, recordedAt), seq); err != nil {
		t.Fatal(err)
	}
	report := mustVerify(t, ctx, s)
	if !report.Intact() {
		t.Fatalf("an older record must not be reported as tampered with: %+v", report.Findings)
	}
	if report.UnprotectedOutputs != 1 {
		t.Fatalf("the operator must be told those outputs are unprotected: %d", report.UnprotectedOutputs)
	}
}
