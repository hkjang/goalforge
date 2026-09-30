package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/goalforge/goalforge/internal/patterns"
)

// SavePattern records a fix, refusing one nobody could act on.
func (s *Store) SavePattern(ctx context.Context, pattern patterns.Pattern) error {
	if err := pattern.Validate(); err != nil {
		return err
	}
	applies, err := json.Marshal(pattern.AppliesWhen)
	if err != nil {
		return err
	}
	decided := ""
	if !pattern.DecidedAt.IsZero() {
		decided = pattern.DecidedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO patterns(id,standard_id,problem,approach,applies_when,source,status,decider,decided_at,retired_reason)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET standard_id=excluded.standard_id,problem=excluded.problem,approach=excluded.approach,
applies_when=excluded.applies_when,source=excluded.source,status=excluded.status,decider=excluded.decider,
decided_at=excluded.decided_at,retired_reason=excluded.retired_reason`,
		pattern.ID, pattern.StandardID, pattern.Problem, pattern.Approach, string(applies), pattern.Source,
		pattern.Status, pattern.Decider, decided, pattern.RetiredReason)
	return err
}

// RecordPatternApplication stores one use of a pattern and how it went.
func (s *Store) RecordPatternApplication(ctx context.Context, application patterns.Application) error {
	switch {
	case application.PatternID == "" || application.ProjectID == "":
		return errors.New("pattern and project are required")
	case application.Outcome != patterns.OutcomePassed && application.Outcome != patterns.OutcomeFailed:
		// An application with no outcome is a note that somebody tried
		// something, and the archive's whole job is to say whether trying it
		// worked.
		return errors.New("적용 결과는 PASSED 또는 FAILED 여야 합니다")
	}
	if application.AppliedAt.IsZero() {
		application.AppliedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO pattern_applications(pattern_id,project_id,work_item_id,commit_sha,outcome,detail,applied_at)
VALUES(?,?,?,?,?,?,?)`,
		application.PatternID, application.ProjectID, application.WorkItemID, application.CommitSHA,
		application.Outcome, application.Detail, application.AppliedAt.UTC().Format(time.RFC3339Nano))
	return err
}

// PatternApplications returns one pattern's uses, oldest first.
//
// Oldest first because the order is what the evidence is made of: a run of
// failures ending in a success means something different from a success
// followed by failures, and a list in the other order says the opposite.
func (s *Store) PatternApplications(ctx context.Context, patternID string) ([]patterns.Application, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pattern_id,project_id,work_item_id,commit_sha,outcome,detail,applied_at
FROM pattern_applications WHERE pattern_id=? ORDER BY applied_at,rowid`, patternID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var applications []patterns.Application
	for rows.Next() {
		var application patterns.Application
		var appliedAt string
		if err = rows.Scan(&application.PatternID, &application.ProjectID, &application.WorkItemID,
			&application.CommitSHA, &application.Outcome, &application.Detail, &appliedAt); err != nil {
			return nil, err
		}
		application.AppliedAt, _ = time.Parse(time.RFC3339Nano, appliedAt)
		applications = append(applications, application)
	}
	return applications, rows.Err()
}

// PatternView is a pattern with what its uses say about it.
type PatternView struct {
	patterns.Pattern
	Evidence patterns.Evidence
}

// Patterns returns every pattern with its evidence.
func (s *Store) Patterns(ctx context.Context) ([]PatternView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,standard_id,problem,approach,applies_when,source,status,decider,decided_at,retired_reason
FROM patterns ORDER BY standard_id,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var views []PatternView
	for rows.Next() {
		var view PatternView
		var applies, decided string
		if err = rows.Scan(&view.ID, &view.StandardID, &view.Problem, &view.Approach, &applies, &view.Source,
			&view.Status, &view.Decider, &decided, &view.RetiredReason); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(applies), &view.AppliesWhen); err != nil {
			return nil, err
		}
		if decided != "" {
			view.DecidedAt, _ = time.Parse(time.RFC3339Nano, decided)
		}
		views = append(views, view)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range views {
		applications, appErr := s.PatternApplications(ctx, views[i].ID)
		if appErr != nil {
			return nil, appErr
		}
		views[i].Evidence = patterns.Summarize(applications)
	}
	return views, nil
}

// PatternByID returns one pattern with its evidence.
func (s *Store) PatternByID(ctx context.Context, id string) (PatternView, error) {
	var view PatternView
	var applies, decided string
	err := s.db.QueryRowContext(ctx, `SELECT id,standard_id,problem,approach,applies_when,source,status,decider,decided_at,retired_reason
FROM patterns WHERE id=?`, id).
		Scan(&view.ID, &view.StandardID, &view.Problem, &view.Approach, &applies, &view.Source,
			&view.Status, &view.Decider, &decided, &view.RetiredReason)
	if errors.Is(err, sql.ErrNoRows) {
		return view, ErrNotFound
	}
	if err != nil {
		return view, err
	}
	if err = json.Unmarshal([]byte(applies), &view.AppliesWhen); err != nil {
		return view, err
	}
	if decided != "" {
		view.DecidedAt, _ = time.Parse(time.RFC3339Nano, decided)
	}
	applications, err := s.PatternApplications(ctx, id)
	if err != nil {
		return view, err
	}
	view.Evidence = patterns.Summarize(applications)
	return view, nil
}
