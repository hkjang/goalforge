package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/goalforge/goalforge/internal/audit"
)

// Chained record kinds. These are the records the product's claims rest on:
// evidence is what says a goal is done, and approvals are what say a change
// was allowed out.
const (
	ChainEvidence = "evidence"
	ChainApproval = "approval"
)

// Integrity findings. They are distinguished because they mean different
// things: a broken link means the history was rewritten, an altered record
// means a row was edited in place, and an unchained record means a row was
// written straight into the database without going through GoalForge at all.
const (
	IntegrityChainBroken  = "CHAIN_BROKEN"
	IntegrityRecordEdited = "RECORD_EDITED"
	IntegrityRecordGone   = "RECORD_MISSING"
	IntegrityUnchained    = "UNCHAINED_RECORD"
)

// IntegrityFinding is one detected discrepancy.
type IntegrityFinding struct {
	Kind, RecordKind, RecordID, Detail string
	Seq                                int64
}

// IntegrityReport is the result of walking the chain.
type IntegrityReport struct {
	Entries  int64
	Findings []IntegrityFinding
	// Keyed says whether the chain is protected by a secret. An unkeyed chain
	// detects edits but can be recomputed by whoever made them; reporting the
	// difference is the difference between a true statement and a boast.
	Keyed bool
}

// Intact reports whether nothing was found.
func (r IntegrityReport) Intact() bool { return len(r.Findings) == 0 }

// appendChain records one link inside the caller's transaction, so a record
// and its chain entry are written together or not at all. Writing them apart
// would create a window where a crash leaves real evidence looking forged.
func appendChain(ctx context.Context, tx *sql.Tx, kind, recordID, payloadDigest, recordedAt string) error {
	var previous string
	err := tx.QueryRowContext(ctx, `SELECT digest FROM audit_chain ORDER BY seq DESC LIMIT 1`).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	digest := audit.LinkDigest(previous, payloadDigest, kind, recordID, recordedAt)
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_chain(kind,record_id,payload_digest,prev_digest,digest,recorded_at) VALUES(?,?,?,?,?,?)`,
		kind, recordID, payloadDigest, previous, digest, recordedAt)
	return err
}

// appendChainDB is appendChain for callers that are not already in a
// transaction. It opens one so the same all-or-nothing rule holds.
func (s *Store) appendChainDB(ctx context.Context, kind, recordID, payloadDigest, recordedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = appendChain(ctx, tx, kind, recordID, payloadDigest, recordedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// evidenceDigest covers the fields that decide what a verification result
// says. Output is included because a result's output is the evidence a person
// reads when deciding whether to trust it.
func evidenceDigest(goalID, runID, checkType, status, actualValue, evidenceKind, createdAt string, required int) string {
	return audit.PayloadDigest(goalID, runID, checkType, status, actualValue, evidenceKind, createdAt, fmt.Sprint(required))
}

func approvalDigest(id, projectID, actionType, status, workItemID, commitSHA, requestedAt string) string {
	return audit.PayloadDigest(id, projectID, actionType, status, workItemID, commitSHA, requestedAt)
}

// VerifyIntegrity walks the chain and compares it against the records it
// covers. It answers three different questions at once: was the history
// rewritten, was a record edited after it was written, and was a record put
// into the database without passing through GoalForge.
func (s *Store) VerifyIntegrity(ctx context.Context) (IntegrityReport, error) {
	report := IntegrityReport{Keyed: audit.Keyed()}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,kind,record_id,payload_digest,prev_digest,digest,recorded_at FROM audit_chain ORDER BY seq`)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	previous := ""
	chained := map[string]map[string]string{ChainEvidence: {}, ChainApproval: {}}
	broken := false
	for rows.Next() {
		var seq int64
		var kind, recordID, payloadDigest, prevDigest, digest, recordedAt string
		if err = rows.Scan(&seq, &kind, &recordID, &payloadDigest, &prevDigest, &digest, &recordedAt); err != nil {
			return report, err
		}
		report.Entries++
		expected := audit.LinkDigest(previous, payloadDigest, kind, recordID, recordedAt)
		if prevDigest != previous || digest != expected {
			// Report the first break only. Everything after it is
			// unverifiable as a consequence, and listing it all would bury
			// the one place the history actually diverges.
			if !broken {
				report.Findings = append(report.Findings, IntegrityFinding{Kind: IntegrityChainBroken,
					RecordKind: kind, RecordID: recordID, Seq: seq,
					Detail: "이 지점부터 기록이 이어지지 않습니다 — 항목이 수정·삽입·삭제되었습니다"})
				broken = true
			}
		}
		previous = digest
		if bucket, ok := chained[kind]; ok {
			bucket[recordID] = payloadDigest
		}
	}
	if err = rows.Err(); err != nil {
		return report, err
	}
	evidenceFindings, err := s.verifyEvidenceRecords(ctx, chained[ChainEvidence])
	if err != nil {
		return report, err
	}
	report.Findings = append(report.Findings, evidenceFindings...)
	approvalFindings, err := s.verifyApprovalRecords(ctx, chained[ChainApproval])
	if err != nil {
		return report, err
	}
	report.Findings = append(report.Findings, approvalFindings...)
	return report, nil
}

func (s *Store) verifyEvidenceRecords(ctx context.Context, chained map[string]string) ([]IntegrityFinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,goal_id,COALESCE(run_id,''),check_type,status,actual_value,COALESCE(evidence_kind,''),created_at,required FROM verification_results ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var findings []IntegrityFinding
	seen := map[string]bool{}
	for rows.Next() {
		var id int64
		var goalID, runID, checkType, status, actualValue, evidenceKind, createdAt string
		var required int
		if err = rows.Scan(&id, &goalID, &runID, &checkType, &status, &actualValue, &evidenceKind, &createdAt, &required); err != nil {
			return nil, err
		}
		recordID := fmt.Sprint(id)
		seen[recordID] = true
		recorded, ok := chained[recordID]
		if !ok {
			findings = append(findings, IntegrityFinding{Kind: IntegrityUnchained, RecordKind: ChainEvidence,
				RecordID: recordID,
				Detail:   fmt.Sprintf("%s=%s 증거가 GoalForge 를 거치지 않고 기록되었습니다", checkType, status)})
			continue
		}
		if recorded != evidenceDigest(goalID, runID, checkType, status, actualValue, evidenceKind, createdAt, required) {
			findings = append(findings, IntegrityFinding{Kind: IntegrityRecordEdited, RecordKind: ChainEvidence,
				RecordID: recordID,
				Detail:   fmt.Sprintf("%s 증거가 기록된 뒤 수정되었습니다 (현재 %s=%s)", checkType, checkType, status)})
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for recordID := range chained {
		if !seen[recordID] {
			findings = append(findings, IntegrityFinding{Kind: IntegrityRecordGone, RecordKind: ChainEvidence,
				RecordID: recordID, Detail: "기록된 증거가 삭제되었습니다"})
		}
	}
	return findings, nil
}

func (s *Store) verifyApprovalRecords(ctx context.Context, chained map[string]string) ([]IntegrityFinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,action_type,status,COALESCE(work_item_id,''),COALESCE(commit_sha,''),requested_at FROM approvals ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var findings []IntegrityFinding
	seen := map[string]bool{}
	for rows.Next() {
		var id, projectID, actionType, status, workItemID, commitSHA, requestedAt string
		if err = rows.Scan(&id, &projectID, &actionType, &status, &workItemID, &commitSHA, &requestedAt); err != nil {
			return nil, err
		}
		seen[id] = true
		recorded, ok := chained[id]
		if !ok {
			findings = append(findings, IntegrityFinding{Kind: IntegrityUnchained, RecordKind: ChainApproval,
				RecordID: id, Detail: fmt.Sprintf("%s 승인이 GoalForge 를 거치지 않고 기록되었습니다", actionType)})
			continue
		}
		if recorded != approvalDigest(id, projectID, actionType, status, workItemID, commitSHA, requestedAt) {
			findings = append(findings, IntegrityFinding{Kind: IntegrityRecordEdited, RecordKind: ChainApproval,
				RecordID: id, Detail: fmt.Sprintf("승인이 기록된 뒤 수정되었습니다 (현재 %s)", status)})
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for recordID := range chained {
		if !seen[recordID] {
			findings = append(findings, IntegrityFinding{Kind: IntegrityRecordGone, RecordKind: ChainApproval,
				RecordID: recordID, Detail: "기록된 승인이 삭제되었습니다"})
		}
	}
	return findings, nil
}

// chainStamp is the timestamp format the chain records, matching the rest of
// the store so a digest can be recomputed from the stored row.
func chainStamp(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }
