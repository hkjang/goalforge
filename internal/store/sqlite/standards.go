package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/standards"
)

// SaveStandardProfile records which pack a project is pinned to, what it has
// declared about itself, and which criteria it has excepted.
//
// The profile is validated against the pack before it is stored. A profile
// holding an exception for a criterion that does not exist, or a required
// criterion excepted with no review condition, would otherwise sit in the
// database looking like a decision somebody made.
func (s *Store) SaveStandardProfile(ctx context.Context, profile standards.Profile, pack standards.Pack) error {
	if err := profile.Validate(pack); err != nil {
		return err
	}
	attributes, err := json.Marshal(profile.Attributes)
	if err != nil {
		return err
	}
	exceptions, err := json.Marshal(profile.Exceptions)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO standard_profiles(project_id,pack_ref,pack_checksum,attributes,exceptions,updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(project_id) DO UPDATE SET pack_ref=excluded.pack_ref,pack_checksum=excluded.pack_checksum,
attributes=excluded.attributes,exceptions=excluded.exceptions,updated_at=excluded.updated_at`,
		profile.ProjectID, profile.PackRef, pack.Checksum(), string(attributes), string(exceptions),
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// StandardProfile reads a project's profile.
func (s *Store) StandardProfile(ctx context.Context, projectID string) (standards.Profile, string, error) {
	profile := standards.Profile{ProjectID: projectID}
	var attributes, exceptions, checksum string
	err := s.db.QueryRowContext(ctx, `SELECT pack_ref,pack_checksum,attributes,exceptions FROM standard_profiles WHERE project_id=?`, projectID).
		Scan(&profile.PackRef, &checksum, &attributes, &exceptions)
	if errors.Is(err, sql.ErrNoRows) {
		return profile, "", ErrNotFound
	}
	if err != nil {
		return profile, "", err
	}
	if err = json.Unmarshal([]byte(attributes), &profile.Attributes); err != nil {
		return profile, checksum, err
	}
	if err = json.Unmarshal([]byte(exceptions), &profile.Exceptions); err != nil {
		return profile, checksum, err
	}
	return profile, checksum, nil
}

// PackDrift reports whether the catalogue a project pinned still has the
// content it had when it was pinned.
//
// The version string alone cannot answer this: a pack edited in place keeps
// its version, and a project would be judged against criteria it never agreed
// to while its profile still says 0.1.
func (s *Store) PackDrift(ctx context.Context, projectID string, pack standards.Pack) (bool, error) {
	_, checksum, err := s.StandardProfile(ctx, projectID)
	if err != nil {
		return false, err
	}
	return checksum != "" && checksum != pack.Checksum(), nil
}

// RecordAssessment stores where one criterion stands at one commit.
//
// The proposed result is put through the judge rather than stored as given, so
// the rule that a guess cannot become MET holds however the assessment was
// produced — by the observer, by a model, or by a person filling in a form.
func (s *Store) RecordAssessment(ctx context.Context, standard standards.Standard, assessment standards.Assessment, excepted bool) (standards.Assessment, error) {
	if assessment.ProjectID == "" || assessment.StandardID == "" {
		return assessment, errors.New("project and criterion are required")
	}
	if assessment.CommitSHA == "" {
		// An assessment not pinned to a commit cannot be re-checked or aged
		// out, and would be applied to code it never saw.
		return assessment, errors.New("an assessment must name the commit it was made against")
	}
	result, detail, err := standards.Judge(standard, assessment.Result, assessment.Evidence, excepted)
	if err != nil {
		return assessment, err
	}
	assessment.Result = result
	if detail != "" {
		assessment.Detail = strings.TrimSpace(detail + " " + assessment.Detail)
	}
	if assessment.AssessedAt.IsZero() {
		assessment.AssessedAt = time.Now().UTC()
	}
	assessment.Revision = standard.Revision
	evidence, err := json.Marshal(assessment.Evidence)
	if err != nil {
		return assessment, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO standard_assessments(project_id,standard_id,revision,commit_sha,result,detail,evidence,tool_version,assessed_at)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(project_id,standard_id) DO UPDATE SET revision=excluded.revision,commit_sha=excluded.commit_sha,
result=excluded.result,detail=excluded.detail,evidence=excluded.evidence,tool_version=excluded.tool_version,
assessed_at=excluded.assessed_at`,
		assessment.ProjectID, assessment.StandardID, assessment.Revision, assessment.CommitSHA, assessment.Result,
		assessment.Detail, string(evidence), assessment.ToolVersion, assessment.AssessedAt.Format(time.RFC3339Nano))
	return assessment, err
}

// AssessmentView is one criterion's standing as a screen shows it.
type AssessmentView struct {
	standards.Assessment
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Category string `json:"category"`
	// Stale is true when the assessment was made against an older revision of
	// the criterion. The result still stands for what it measured; it just no
	// longer answers the question currently being asked — so Result goes back
	// to UNKNOWN and what it used to say moves to PriorResult.
	//
	// It is reset here rather than left to each reader. Six places consume
	// these views and exactly one of them remembered to check this flag, which
	// is how a fleet report stayed green through a catalogue upgrade that
	// moved the bar.
	Stale bool `json:"stale"`
	// PriorResult is what the superseded assessment said, kept so a board can
	// show the criterion was once met. A view that forgets reads as never
	// assessed, and then nobody can tell an upgrade from a regression.
	PriorResult string `json:"prior_result,omitempty"`
}

// AssessmentsFor returns every criterion in force for a project together with
// where it stands, including the ones nothing has looked at yet.
//
// Criteria with no assessment are returned as UNKNOWN rather than omitted. A
// list that only shows what was measured reads as complete coverage of a
// catalogue nobody finished walking.
func (s *Store) AssessmentsFor(ctx context.Context, projectID string, pack standards.Pack, profile standards.Profile, now time.Time) ([]AssessmentView, error) {
	stored := map[string]standards.Assessment{}
	rows, err := s.db.QueryContext(ctx, `SELECT standard_id,revision,commit_sha,result,detail,evidence,tool_version,assessed_at FROM standard_assessments WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry standards.Assessment
		var evidence, assessedAt string
		if err = rows.Scan(&entry.StandardID, &entry.Revision, &entry.CommitSHA, &entry.Result, &entry.Detail,
			&evidence, &entry.ToolVersion, &assessedAt); err != nil {
			return nil, err
		}
		entry.ProjectID = projectID
		entry.AssessedAt, _ = time.Parse(time.RFC3339Nano, assessedAt)
		if err = json.Unmarshal([]byte(evidence), &entry.Evidence); err != nil {
			return nil, err
		}
		stored[entry.StandardID] = entry
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	var views []AssessmentView
	for _, applicable := range standards.Apply(pack, profile, now) {
		if applicable.OutOfProfile {
			continue
		}
		standard := applicable.Standard
		view := AssessmentView{Title: standard.Title, Severity: standard.Severity, Category: standard.Category}
		entry, measured := stored[standard.ID]
		switch {
		case applicable.Excepted:
			view.Assessment = standards.Assessment{ProjectID: projectID, StandardID: standard.ID,
				Revision: standard.Revision, Result: standards.ResultNotApplicable,
				Detail: applicable.Exception.Reason + " (" + applicable.Exception.Decider + ")"}
		case measured && entry.Revision != standard.Revision:
			// The criterion was revised after this was judged. The stored
			// result was about different text, so it settles nothing here.
			view.Assessment = entry
			view.Stale, view.PriorResult = true, entry.Result
			view.Result = standards.ResultUnknown
			view.Detail = fmt.Sprintf("기준이 rev %d 로 개정되었습니다 — 이전 판정(%s, rev %d)은 다른 기준에 대한 것입니다",
				standard.Revision, entry.Result, entry.Revision)
		case measured:
			view.Assessment = entry
		default:
			view.Assessment = standards.Assessment{ProjectID: projectID, StandardID: standard.ID,
				Revision: standard.Revision, Result: standards.ResultUnknown,
				Detail: "아직 확인하지 않았습니다"}
		}
		views = append(views, view)
	}
	return views, nil
}

// ErrDuplicateFinding means a candidate for the same defect is already on the
// board.
var ErrDuplicateFinding = errors.New("this finding is already filed")

// FileFinding records a candidate against its dedup key, refusing one that
// repeats a finding already filed.
//
// The key is the defect, not the wording. A generator asked for new ideas will
// produce a new sentence about the same gap every cycle, and a board that
// accepts each one buries the real backlog under paraphrases.
func (s *Store) FileFinding(ctx context.Context, projectID, standardID, defectKind, targetScope, workItemID string) error {
	key := standards.DedupKey(projectID, standardID, defectKind, targetScope)
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT work_item_id FROM standard_findings WHERE dedup_key=?`, key).Scan(&existing)
	switch {
	case err == nil:
		return fmt.Errorf("%w: %s", ErrDuplicateFinding, existing)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO standard_findings(dedup_key,project_id,standard_id,defect_kind,target_scope,work_item_id,filed_at) VALUES(?,?,?,?,?,?,?)`,
		key, projectID, standardID, defectKind, targetScope, workItemID, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// FindingFor returns the work item already filed for a defect, if any.
func (s *Store) FindingFor(ctx context.Context, projectID, standardID, defectKind, targetScope string) (string, error) {
	var workItemID string
	err := s.db.QueryRowContext(ctx, `SELECT work_item_id FROM standard_findings WHERE dedup_key=?`,
		standards.DedupKey(projectID, standardID, defectKind, targetScope)).Scan(&workItemID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return workItemID, err
}

// OutstandingSuppliedWork counts the work items filed from a finding that are
// still unfinished.
//
// It is the brake on supply. A board nobody is working through does not need
// more findings on it; it needs the ones it has closed, and a supplier that
// keeps adding while nothing leaves is generating a backlog rather than
// progress.
func (s *Store) OutstandingSuppliedWork(ctx context.Context, projectID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM standard_findings f
JOIN work_items w ON w.id=f.work_item_id
WHERE f.project_id=? AND w.status NOT IN ('DONE','DISCARDED')`, projectID).Scan(&count)
	return count, err
}

// RunnableWorkCount is how many items could be picked up right now.
//
// It counts what is actually available — approved or backlogged, owned by
// automation, with every predecessor done — rather than everything on the
// board. A board holding forty items that all wait on one another is an empty
// board from the runner's point of view, and a supplier that counted rows
// would never notice the project had stalled.
func (s *Store) RunnableWorkCount(ctx context.Context, goalID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items w
WHERE w.goal_id=? AND w.status IN ('APPROVED','BACKLOG') AND COALESCE(w.owner,'AI')='AI'
AND NOT EXISTS(SELECT 1 FROM work_item_dependencies d LEFT JOIN work_items p ON p.id=d.depends_on_id
               WHERE d.work_item_id=w.id AND COALESCE(p.status,'')<>'DONE')`, goalID).Scan(&count)
	return count, err
}
