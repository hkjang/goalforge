package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// PageCapture is one screenshot and what it is a picture of.
//
// The commit is the point. A screenshot is a claim about how the product looks
// now, and without the commit it was taken at there is no way to ask whether
// the screen has moved since — the file's timestamp says when somebody ran the
// capture, not what it depicts.
type PageCapture struct {
	ProjectID, PageKey            string
	Route, Role, State            string
	CommitSHA, ArtifactPath       string
	RunID, ToolVersion, ServiceAt string
	CapturedAt                    time.Time
}

// RecordCapture stores a screenshot against the commit it depicts.
func (s *Store) RecordCapture(ctx context.Context, capture PageCapture) error {
	switch {
	case capture.ProjectID == "" || capture.PageKey == "":
		return errors.New("project and page are required")
	case capture.CommitSHA == "":
		return errors.New("a capture must name the commit it depicts")
	case strings.TrimSpace(capture.ArtifactPath) == "":
		// A capture that produced no file is not a capture. Recording it would
		// put a page in the "done" column with nothing behind it.
		return errors.New("a capture with no image is not a capture")
	}
	if capture.CapturedAt.IsZero() {
		capture.CapturedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO page_captures(project_id,page_key,route,role,state,commit_sha,artifact_path,run_id,tool_version,service_at,captured_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(project_id,page_key) DO UPDATE SET route=excluded.route,role=excluded.role,state=excluded.state,
commit_sha=excluded.commit_sha,artifact_path=excluded.artifact_path,run_id=excluded.run_id,
tool_version=excluded.tool_version,service_at=excluded.service_at,captured_at=excluded.captured_at`,
		capture.ProjectID, capture.PageKey, capture.Route, capture.Role, capture.State, capture.CommitSHA,
		capture.ArtifactPath, capture.RunID, capture.ToolVersion, capture.ServiceAt,
		capture.CapturedAt.Format(time.RFC3339Nano))
	return err
}

// Captures is every screenshot recorded for a project, keyed by page.
func (s *Store) Captures(ctx context.Context, projectID string) (map[string]PageCapture, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT page_key,route,role,state,commit_sha,artifact_path,run_id,tool_version,service_at,captured_at
FROM page_captures WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	captures := map[string]PageCapture{}
	for rows.Next() {
		capture := PageCapture{ProjectID: projectID}
		var capturedAt string
		if err = rows.Scan(&capture.PageKey, &capture.Route, &capture.Role, &capture.State, &capture.CommitSHA,
			&capture.ArtifactPath, &capture.RunID, &capture.ToolVersion, &capture.ServiceAt, &capturedAt); err != nil {
			return nil, err
		}
		capture.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		captures[capture.PageKey] = capture
	}
	return captures, rows.Err()
}

// CaptureFor returns one page's screenshot.
func (s *Store) CaptureFor(ctx context.Context, projectID, pageKey string) (PageCapture, error) {
	capture := PageCapture{ProjectID: projectID, PageKey: pageKey}
	var capturedAt string
	err := s.db.QueryRowContext(ctx, `SELECT route,role,state,commit_sha,artifact_path,run_id,tool_version,service_at,captured_at
FROM page_captures WHERE project_id=? AND page_key=?`, projectID, pageKey).
		Scan(&capture.Route, &capture.Role, &capture.State, &capture.CommitSHA, &capture.ArtifactPath,
			&capture.RunID, &capture.ToolVersion, &capture.ServiceAt, &capturedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return capture, ErrNotFound
	}
	if err == nil {
		capture.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	}
	return capture, err
}
