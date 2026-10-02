package sqlite

import (
	"context"
	"errors"
	"strings"
	"time"
)

// StateOverride is one deliberate state change a person made.
//
// Recorded because the question asked later is not what state the project is
// in but why somebody decided it could be moved. A state change nobody has to
// justify is one that gets made to quiet an error rather than to fix it, and
// the next reader cannot tell which happened.
type StateOverride struct {
	ID, ProjectID, From, To, Reason string
	CreatedAt                       time.Time
}

// BlockProject puts a project into BLOCKED with the reason it was blocked.
//
// It exists so a policy violation has one way to block a project, rather than
// each caller writing the UPDATE and choosing which states it comes from.
func (s *Store) BlockProject(ctx context.Context, projectID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("blocking a project needs a reason")
	}
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		return err
	}
	if project.State == "BLOCKED" {
		return nil
	}
	if err = s.TransitionProjectState(ctx, projectID, project.State, "BLOCKED"); err != nil {
		return err
	}
	return s.recordOverride(ctx, projectID, project.State, "BLOCKED", reason)
}

// UnblockProject returns a blocked project to READY.
//
// There was no way back. The only transition out of BLOCKED was the checkpoint
// resume, and a run stopped by a policy violation never checkpointed — so the
// resume failed looking for one and the project stayed blocked forever, even
// after the thing that caused the violation was fixed.
//
// The reason is required. Clearing a block is a judgement that whatever stopped
// the project has been dealt with, and that judgement is the only thing
// standing between a violation and ignoring it.
func (s *Store) UnblockProject(ctx context.Context, projectID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("막힘을 푸는 데는 이유가 필요합니다 — 무엇을 고쳤는지 적으세요")
	}
	project, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		return err
	}
	if project.State != "BLOCKED" {
		// Refused rather than reported as done. Saying it worked when nothing
		// changed lets an operator believe they cleared something.
		return errors.New("이 프로젝트는 막혀 있지 않습니다 (현재 " + project.State + ")")
	}
	if err = s.TransitionProjectState(ctx, projectID, "BLOCKED", "READY"); err != nil {
		return err
	}
	// A run stopped part-way leaves its work item IN_PROGRESS and nothing moves
	// it back: a manual transition out of an engine phase is refused, because
	// the run is supposed to still be going. After a block it is not going,
	// and the item holds the WIP slot — so the next run is refused with "WIP
	// limit reached" and the project cannot progress even once the block is
	// cleared.
	//
	// Returned to BACKLOG, which is what the quota path already does with an
	// item it interrupts. Two answers to the same question would be two rules.
	if _, err = s.db.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG'
WHERE status='IN_PROGRESS' AND goal_id IN (SELECT id FROM goals WHERE project_id=?)`, projectID); err != nil {
		return err
	}
	return s.recordOverride(ctx, projectID, "BLOCKED", "READY", reason)
}

func (s *Store) recordOverride(ctx context.Context, projectID, from, to, reason string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_state_overrides(id,project_id,from_state,to_state,reason,created_at)
VALUES(?,?,?,?,?,?)`, NewID("OVR"), projectID, from, to, reason,
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// ProjectStateHistory lists the deliberate state changes, newest last.
func (s *Store) ProjectStateHistory(ctx context.Context, projectID string) ([]StateOverride, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,from_state,to_state,reason,created_at
FROM project_state_overrides WHERE project_id=? ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var history []StateOverride
	for rows.Next() {
		var entry StateOverride
		var createdAt string
		if err = rows.Scan(&entry.ID, &entry.ProjectID, &entry.From, &entry.To, &entry.Reason, &createdAt); err != nil {
			return nil, err
		}
		entry.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		history = append(history, entry)
	}
	return history, rows.Err()
}
