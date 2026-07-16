package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/goalforge/goalforge/internal/audit"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/provider"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db       *sql.DB
	stateDir string
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, stateDir: filepath.Dir(path)}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS projects (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, repository_path TEXT NOT NULL UNIQUE,
 default_branch TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('CREATED','READY','PREFLIGHT','RUNNING','DRAINING','VERIFYING','REPAIR_REQUIRED','CHECKPOINTING','WAITING_QUOTA','RESUMING','BLOCKED','FAILED','COMPLETED','CANCELLED')),
 worktree_enabled INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS goals (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), version INTEGER NOT NULL,
 title TEXT NOT NULL, objective TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'ACTIVE',
 change_reason TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, UNIQUE(project_id, version)
);
CREATE TABLE IF NOT EXISTS goal_criteria (
 goal_id TEXT NOT NULL REFERENCES goals(id) ON DELETE CASCADE, criterion_type TEXT NOT NULL,
 expected_value TEXT NOT NULL, PRIMARY KEY(goal_id, criterion_type)
);
CREATE TABLE IF NOT EXISTS milestones (
 id TEXT PRIMARY KEY, goal_id TEXT NOT NULL REFERENCES goals(id), title TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'PENDING', weight REAL NOT NULL DEFAULT 1 CHECK(weight > 0)
);
CREATE TABLE IF NOT EXISTS work_items (
 id TEXT PRIMARY KEY, goal_id TEXT NOT NULL REFERENCES goals(id), milestone_id TEXT REFERENCES milestones(id),
 type TEXT NOT NULL, title TEXT NOT NULL, priority REAL NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'BACKLOG',
 dependency TEXT NOT NULL DEFAULT '', risk TEXT NOT NULL DEFAULT 'medium', change_scope TEXT NOT NULL DEFAULT '',
 weight REAL NOT NULL DEFAULT 1 CHECK(weight > 0),
 estimated_tokens INTEGER NOT NULL DEFAULT 0 CHECK(estimated_tokens >= 0)
);
CREATE TABLE IF NOT EXISTS verification_results (
 id INTEGER PRIMARY KEY AUTOINCREMENT, goal_id TEXT NOT NULL REFERENCES goals(id), run_id TEXT,
 check_type TEXT NOT NULL, status TEXT NOT NULL, actual_value TEXT NOT NULL DEFAULT '',
 command TEXT NOT NULL DEFAULT '', exit_code INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0,
 required INTEGER NOT NULL DEFAULT 1, output TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS provider_sessions (
 project_id TEXT NOT NULL REFERENCES projects(id), provider TEXT NOT NULL, session_id TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'ACTIVE', last_run_id TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,provider), UNIQUE(provider,session_id)
);
CREATE TABLE IF NOT EXISTS provider_session_history (
 project_id TEXT NOT NULL REFERENCES projects(id), provider TEXT NOT NULL, session_id TEXT NOT NULL,
 model TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'ACTIVE', last_run_id TEXT NOT NULL DEFAULT '',
 context_tokens_used INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 expires_at TEXT, retention_until TEXT, replacement_reason TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(provider,session_id)
);
CREATE TABLE IF NOT EXISTS provider_handoffs (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), from_provider TEXT NOT NULL,
 to_provider TEXT NOT NULL, to_model TEXT NOT NULL DEFAULT '', goal_version INTEGER NOT NULL,
 reason TEXT NOT NULL, content_json TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'PENDING',
 created_at TEXT NOT NULL, consumed_run_id TEXT
);
CREATE TABLE IF NOT EXISTS worktrees (
 project_id TEXT NOT NULL REFERENCES projects(id), work_item_id TEXT NOT NULL, path TEXT NOT NULL,
 branch TEXT NOT NULL, base_commit TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'ACTIVE',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(project_id,work_item_id), UNIQUE(path)
);
CREATE TABLE IF NOT EXISTS rollback_records (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), work_item_id TEXT NOT NULL,
 target_commit TEXT NOT NULL, path TEXT NOT NULL, branch TEXT NOT NULL, reason TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_commits (
 run_id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), goal_id TEXT NOT NULL,
 work_item_id TEXT NOT NULL, commit_sha TEXT NOT NULL, branch TEXT NOT NULL,
 files_committed INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_session_active
 ON provider_session_history(project_id,provider) WHERE status='ACTIVE';
CREATE TABLE IF NOT EXISTS runs (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), work_item_id TEXT,
 provider TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', state TEXT NOT NULL,
 started_at TEXT NOT NULL, ended_at TEXT
);
CREATE TABLE IF NOT EXISTS event_logs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL REFERENCES runs(id), provider TEXT NOT NULL,
 event_type TEXT NOT NULL, event_key TEXT NOT NULL, raw_hash TEXT NOT NULL, raw_payload BLOB NOT NULL,
 created_at TEXT NOT NULL, UNIQUE(run_id,raw_hash)
);
CREATE TABLE IF NOT EXISTS prompt_records (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL UNIQUE REFERENCES runs(id), template TEXT NOT NULL,
 rendered_hash TEXT NOT NULL, redacted_prompt TEXT NOT NULL, encrypted_prompt BLOB, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_file_changes (
 run_id TEXT NOT NULL REFERENCES runs(id), path TEXT NOT NULL, change_type TEXT NOT NULL,
 before_hash TEXT NOT NULL DEFAULT '', after_hash TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 PRIMARY KEY(run_id,path)
);
CREATE TABLE IF NOT EXISTS approvals (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), action_type TEXT NOT NULL,
 reason TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('PENDING','APPROVED','CONSUMED','REJECTED')),
 requested_at TEXT NOT NULL, approved_at TEXT, consumed_run_id TEXT
);
CREATE TABLE IF NOT EXISTS policy_violations (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), run_id TEXT NOT NULL REFERENCES runs(id),
 policy_type TEXT NOT NULL, details TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS usage_ledger (
 id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL REFERENCES runs(id), event_key TEXT NOT NULL,
 token_type TEXT NOT NULL, amount INTEGER NOT NULL DEFAULT 0, cost REAL NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL, UNIQUE(run_id,event_key,token_type)
);
CREATE TABLE IF NOT EXISTS project_budgets (
 project_id TEXT PRIMARY KEY REFERENCES projects(id), token_limit INTEGER NOT NULL DEFAULT 0,
 cost_limit_usd REAL NOT NULL DEFAULT 0, daily_run_limit INTEGER NOT NULL DEFAULT 0,
 daily_token_limit INTEGER NOT NULL DEFAULT 0, daily_cost_limit_usd REAL NOT NULL DEFAULT 0,
 updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_policies (
 project_id TEXT PRIMARY KEY REFERENCES projects(id), turn_timeout_seconds INTEGER NOT NULL,
 run_timeout_seconds INTEGER NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS quota_windows (
 provider TEXT NOT NULL, account_id TEXT NOT NULL, limit_type TEXT NOT NULL, status TEXT NOT NULL,
 used_percent REAL NOT NULL, detected_at TEXT NOT NULL, quota_reset_at TEXT, resume_at TEXT,
 source TEXT NOT NULL, confidence TEXT NOT NULL, raw_message TEXT NOT NULL,
 PRIMARY KEY(provider,account_id,limit_type)
);
CREATE TABLE IF NOT EXISTS checkpoints (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), run_id TEXT,
 goal_version INTEGER NOT NULL, work_item_id TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL,
 model TEXT NOT NULL DEFAULT '', session_id TEXT NOT NULL DEFAULT '', commit_sha TEXT NOT NULL DEFAULT '',
 branch TEXT NOT NULL DEFAULT '', dirty_files TEXT NOT NULL DEFAULT '[]', dirty_fingerprint TEXT NOT NULL DEFAULT '',
 completed_summary TEXT NOT NULL DEFAULT '',
 verification_summary TEXT NOT NULL DEFAULT '', remaining_steps TEXT NOT NULL DEFAULT '',
 next_action TEXT NOT NULL, risk_summary TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS scheduler_jobs (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), job_type TEXT NOT NULL,
 run_at TEXT NOT NULL, idempotency_key TEXT NOT NULL UNIQUE, status TEXT NOT NULL DEFAULT 'PENDING',
 payload TEXT NOT NULL DEFAULT '{}', attempts INTEGER NOT NULL DEFAULT 0, owner TEXT NOT NULL DEFAULT '',
 lease_until TEXT, last_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS process_leases (
 project_id TEXT PRIMARY KEY REFERENCES projects(id), owner TEXT NOT NULL, expires_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_control_requests (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), run_id TEXT NOT NULL REFERENCES runs(id),
 action TEXT NOT NULL CHECK(action IN ('PAUSE','CANCEL')), status TEXT NOT NULL DEFAULT 'PENDING',
 requested_at TEXT NOT NULL, handled_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_run_control ON run_control_requests(run_id,action) WHERE status='PENDING';
CREATE TABLE IF NOT EXISTS idea_scores (
 work_item_id TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE CASCADE,
 goal_contribution REAL NOT NULL, user_value REAL NOT NULL, operational_need REAL NOT NULL,
 feasibility REAL NOT NULL, risk_reduction REAL NOT NULL, difficulty REAL NOT NULL,
 priority_score REAL NOT NULL, expected_change_scope TEXT NOT NULL, fingerprint TEXT NOT NULL,
 scope_expansion INTEGER NOT NULL DEFAULT 0, approval_required INTEGER NOT NULL DEFAULT 0,
 low_score_cycles INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_idea_fingerprint ON idea_scores(fingerprint);
CREATE TABLE IF NOT EXISTS loop_signals (
 project_id TEXT NOT NULL REFERENCES projects(id), work_item_id TEXT NOT NULL DEFAULT '',
 signal_type TEXT NOT NULL, fingerprint TEXT NOT NULL, occurrences INTEGER NOT NULL DEFAULT 1,
 last_run_id TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,work_item_id,signal_type,fingerprint)
);
CREATE TABLE IF NOT EXISTS verification_gates (
 project_id TEXT NOT NULL REFERENCES projects(id), check_type TEXT NOT NULL,
 command_json TEXT NOT NULL, timeout_seconds INTEGER NOT NULL, required INTEGER NOT NULL DEFAULT 1,
 success_value TEXT NOT NULL DEFAULT 'true', created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,check_type)
);
CREATE INDEX IF NOT EXISTS idx_goals_project_version ON goals(project_id, version DESC);
CREATE INDEX IF NOT EXISTS idx_work_goal_status ON work_items(goal_id, status);
CREATE INDEX IF NOT EXISTS idx_verify_goal_type ON verification_results(goal_id, check_type, id DESC);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	columns := []struct{ name, definition string }{{"run_id", "TEXT"}, {"command", "TEXT NOT NULL DEFAULT ''"}, {"exit_code", "INTEGER NOT NULL DEFAULT 0"}, {"duration_ms", "INTEGER NOT NULL DEFAULT 0"}, {"required", "INTEGER NOT NULL DEFAULT 1"}}
	for _, column := range columns {
		if err := s.ensureColumn(ctx, "verification_results", column.name, column.definition); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "checkpoints", "dirty_fingerprint", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "work_items", "estimated_tokens", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "work_items", "change_scope", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "projects", "worktree_enabled", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "projects", "auto_commit_enabled", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "runs", "task_type", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	for _, column := range []struct{ name, definition string }{{"daily_run_limit", "INTEGER NOT NULL DEFAULT 0"}, {"daily_token_limit", "INTEGER NOT NULL DEFAULT 0"}, {"daily_cost_limit_usd", "REAL NOT NULL DEFAULT 0"}} {
		if err := s.ensureColumn(ctx, "project_budgets", column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO provider_session_history(project_id,provider,session_id,status,last_run_id,created_at,updated_at) SELECT project_id,provider,session_id,status,last_run_id,updated_at,updated_at FROM provider_sessions`); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureColumn(ctx context.Context, table, name, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var column, kind string
		var notNull, pk int
		var defaultValue any
		if err = rows.Scan(&cid, &column, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if column == name {
			found = true
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+name+" "+definition)
	return err
}

func NewID(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano()) }

func (s *Store) CreateProject(ctx context.Context, p model.Project) error {
	if p.ID == "" {
		p.ID = NewID("PRJ")
	}
	if p.State == "" {
		p.State = "CREATED"
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects(id,name,repository_path,default_branch,provider,model,state,worktree_enabled,auto_commit_enabled,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.RepositoryPath, p.DefaultBranch, p.Provider, p.Model, p.State, p.WorktreeEnabled, p.AutoCommitEnabled, p.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) ProjectByPath(ctx context.Context, path string) (model.Project, error) {
	path, _ = filepath.Abs(path)
	var p model.Project
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,repository_path,default_branch,provider,model,state,worktree_enabled,auto_commit_enabled,created_at FROM projects WHERE repository_path=?`, path).
		Scan(&p.ID, &p.Name, &p.RepositoryPath, &p.DefaultBranch, &p.Provider, &p.Model, &p.State, &p.WorktreeEnabled, &p.AutoCommitEnabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err == nil {
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	}
	return p, err
}

func (s *Store) ProjectByID(ctx context.Context, id string) (model.Project, error) {
	var p model.Project
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,repository_path,default_branch,provider,model,state,worktree_enabled,auto_commit_enabled,created_at FROM projects WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.RepositoryPath, &p.DefaultBranch, &p.Provider, &p.Model, &p.State, &p.WorktreeEnabled, &p.AutoCommitEnabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err == nil {
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	}
	return p, err
}

func (s *Store) ListProjects(ctx context.Context) ([]model.Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,repository_path,default_branch,provider,model,state,worktree_enabled,auto_commit_enabled,created_at FROM projects ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []model.Project
	for rows.Next() {
		var project model.Project
		var created string
		if err = rows.Scan(&project.ID, &project.Name, &project.RepositoryPath, &project.DefaultBranch, &project.Provider, &project.Model, &project.State, &project.WorktreeEnabled, &project.AutoCommitEnabled, &created); err != nil {
			return nil, err
		}
		project.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (s *Store) SetGoal(ctx context.Context, projectID, title, objective, reason string, criteria []model.Criterion) (model.Goal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Goal{}, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM goals WHERE project_id=?`, projectID).Scan(&version); err != nil {
		return model.Goal{}, err
	}
	if version > 1 && strings.TrimSpace(reason) == "" {
		return model.Goal{}, errors.New("change reason is required for a new goal version")
	}
	g := model.Goal{ID: NewID("GOAL"), ProjectID: projectID, Version: version, Title: title, Objective: objective, Status: "ACTIVE", ChangeReason: reason, CreatedAt: time.Now().UTC(), Criteria: criteria}
	if _, err = tx.ExecContext(ctx, `UPDATE goals SET status='SUPERSEDED' WHERE project_id=? AND status='ACTIVE'`, projectID); err != nil {
		return g, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO goals(id,project_id,version,title,objective,status,change_reason,created_at) VALUES(?,?,?,?,?,?,?,?)`, g.ID, g.ProjectID, g.Version, g.Title, g.Objective, g.Status, g.ChangeReason, g.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return g, err
	}
	for _, c := range criteria {
		if _, err = tx.ExecContext(ctx, `INSERT INTO goal_criteria(goal_id,criterion_type,expected_value) VALUES(?,?,?)`, g.ID, c.Type, c.ExpectedValue); err != nil {
			return g, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET state='READY' WHERE id=?`, projectID); err != nil {
		return g, err
	}
	return g, tx.Commit()
}

func (s *Store) CurrentGoal(ctx context.Context, projectID string) (model.Goal, error) {
	return s.loadGoal(ctx, `SELECT id,project_id,version,title,objective,status,change_reason,created_at FROM goals WHERE project_id=? AND status='ACTIVE' ORDER BY version DESC LIMIT 1`, projectID)
}

func (s *Store) LatestGoal(ctx context.Context, projectID string) (model.Goal, error) {
	return s.loadGoal(ctx, `SELECT id,project_id,version,title,objective,status,change_reason,created_at FROM goals WHERE project_id=? ORDER BY version DESC LIMIT 1`, projectID)
}

func (s *Store) loadGoal(ctx context.Context, query, projectID string) (model.Goal, error) {
	var g model.Goal
	var created string
	err := s.db.QueryRowContext(ctx, query, projectID).
		Scan(&g.ID, &g.ProjectID, &g.Version, &g.Title, &g.Objective, &g.Status, &g.ChangeReason, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, err
	}
	g.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	rows, err := s.db.QueryContext(ctx, `SELECT criterion_type,expected_value FROM goal_criteria WHERE goal_id=? ORDER BY criterion_type`, g.ID)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var c model.Criterion
		if err = rows.Scan(&c.Type, &c.ExpectedValue); err != nil {
			return g, err
		}
		g.Criteria = append(g.Criteria, c)
	}
	return g, rows.Err()
}

func (s *Store) GoalProgress(ctx context.Context, goal model.Goal) (float64, bool, error) {
	var total, done float64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(weight),0),COALESCE(SUM(CASE WHEN status='DONE' THEN weight ELSE 0 END),0) FROM work_items WHERE goal_id=?`, goal.ID).Scan(&total, &done); err != nil {
		return 0, false, err
	}
	criteriaOK := len(goal.Criteria) > 0
	for _, c := range goal.Criteria {
		var actual, status string
		err := s.db.QueryRowContext(ctx, `SELECT actual_value,status FROM verification_results WHERE goal_id=? AND check_type=? ORDER BY id DESC LIMIT 1`, goal.ID, c.Type).Scan(&actual, &status)
		if err != nil || status != "PASSED" || !criterionMet(c.ExpectedValue, actual) {
			criteriaOK = false
		}
	}
	progress := float64(0)
	if total > 0 {
		progress = done / total * 100
	}
	return progress, criteriaOK && total > 0 && done == total, nil
}

func (s *Store) CreateMilestone(ctx context.Context, m model.Milestone) (model.Milestone, error) {
	if strings.TrimSpace(m.Title) == "" {
		return m, errors.New("milestone title is required")
	}
	if m.ID == "" {
		m.ID = NewID("MILE")
	}
	if m.Status == "" {
		m.Status = "PENDING"
	}
	if m.Weight <= 0 {
		m.Weight = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO milestones(id,goal_id,title,status,weight) VALUES(?,?,?,?,?)`, m.ID, m.GoalID, m.Title, m.Status, m.Weight)
	return m, err
}

func (s *Store) ListMilestones(ctx context.Context, goalID string) ([]model.Milestone, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,goal_id,title,status,weight FROM milestones WHERE goal_id=? ORDER BY id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.Milestone
	for rows.Next() {
		var m model.Milestone
		if err := rows.Scan(&m.ID, &m.GoalID, &m.Title, &m.Status, &m.Weight); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *Store) CreateWorkItem(ctx context.Context, w model.WorkItem) (model.WorkItem, error) {
	if strings.TrimSpace(w.Title) == "" || strings.TrimSpace(w.Type) == "" {
		return w, errors.New("work item title and type are required")
	}
	if w.ID == "" {
		w.ID = NewID("WORK")
	}
	if w.Status == "" {
		w.Status = "BACKLOG"
	}
	if w.Weight <= 0 {
		w.Weight = 1
	}
	if w.Risk == "" {
		w.Risk = "medium"
	}
	var milestone any
	if w.MilestoneID != "" {
		milestone = w.MilestoneID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO work_items(id,goal_id,milestone_id,type,title,priority,status,dependency,risk,change_scope,weight,estimated_tokens) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, w.ID, w.GoalID, milestone, w.Type, w.Title, w.Priority, w.Status, w.Dependency, w.Risk, w.ChangeScope, w.Weight, w.EstimatedTokens)
	return w, err
}

func (s *Store) ListWorkItems(ctx context.Context, goalID string) ([]model.WorkItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,goal_id,COALESCE(milestone_id,''),type,title,priority,status,dependency,risk,change_scope,weight,estimated_tokens FROM work_items WHERE goal_id=? ORDER BY CASE status WHEN 'IN_PROGRESS' THEN 0 WHEN 'BACKLOG' THEN 1 ELSE 2 END, priority DESC, id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.WorkItem
	for rows.Next() {
		var w model.WorkItem
		if err := rows.Scan(&w.ID, &w.GoalID, &w.MilestoneID, &w.Type, &w.Title, &w.Priority, &w.Status, &w.Dependency, &w.Risk, &w.ChangeScope, &w.Weight, &w.EstimatedTokens); err != nil {
			return nil, err
		}
		result = append(result, w)
	}
	return result, rows.Err()
}

func (s *Store) SetWorkItemStatus(ctx context.Context, goalID, workID, status string) error {
	allowed := map[string]bool{"BACKLOG": true, "APPROVED": true, "IN_PROGRESS": true, "VERIFYING": true, "DONE": true, "BLOCKED": true, "DISCARDED": true}
	if !allowed[status] {
		return fmt.Errorf("invalid work item status %q", status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var dependency string
	if err := tx.QueryRowContext(ctx, `SELECT dependency FROM work_items WHERE id=? AND goal_id=?`, workID, goalID).Scan(&dependency); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if status == "IN_PROGRESS" {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE goal_id=? AND status='IN_PROGRESS' AND id<>?`, goalID, workID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return errors.New("implementation WIP limit reached: another item is in progress")
		}
		if dependency != "" {
			var dependencyStatus string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM work_items WHERE id=? AND goal_id=?`, dependency, goalID).Scan(&dependencyStatus); err != nil {
				return fmt.Errorf("dependency %s is unavailable: %w", dependency, err)
			}
			if dependencyStatus != "DONE" {
				return fmt.Errorf("dependency %s is not done", dependency)
			}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET status=? WHERE id=? AND goal_id=?`, status, workID, goalID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) RecordVerification(ctx context.Context, goalID, checkType, status, actual, output string) error {
	if status != "PASSED" && status != "FAILED" && status != "UNKNOWN" {
		return fmt.Errorf("invalid verification status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO verification_results(goal_id,check_type,status,actual_value,output,created_at) VALUES(?,?,?,?,?,?)`, goalID, checkType, status, actual, audit.RedactString(output), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func criterionMet(expected, actual string) bool {
	if expected == actual {
		return true
	}
	e, eerr := strconv.ParseFloat(expected, 64)
	a, aerr := strconv.ParseFloat(actual, 64)
	return eerr == nil && aerr == nil && a >= e
}

type RunRecord struct {
	ID, ProjectID, WorkItemID, Provider, Model, State string
	TaskType                                          string
}

type SessionRecord struct {
	ProjectID, Provider, SessionID, Model, Status, LastRunID, ReplacementReason string
	ContextTokensUsed                                                           int64
	CreatedAt, UpdatedAt, ExpiresAt, RetentionUntil                             time.Time
}

type PromptRecord struct {
	RunID, Template, RenderedHash, RedactedPrompt string
	EncryptedPrompt                               []byte
}

func (s *Store) RecordPrompt(ctx context.Context, runID, template, prompt string) error {
	if runID == "" || prompt == "" {
		return errors.New("run ID and prompt are required")
	}
	if template == "" {
		template = "execution"
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(prompt)))
	encrypted, err := audit.EncryptFromEnvironment([]byte(prompt))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO prompt_records(id,run_id,template,rendered_hash,redacted_prompt,encrypted_prompt,created_at) VALUES(?,?,?,?,?,?,?)`, NewID("PROMPT"), runID, template, hash, audit.RedactString(prompt), encrypted, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) PromptRecord(ctx context.Context, runID string) (PromptRecord, error) {
	var record PromptRecord
	err := s.db.QueryRowContext(ctx, `SELECT run_id,template,rendered_hash,redacted_prompt,COALESCE(encrypted_prompt,X'') FROM prompt_records WHERE run_id=?`, runID).Scan(&record.RunID, &record.Template, &record.RenderedHash, &record.RedactedPrompt, &record.EncryptedPrompt)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	return record, err
}

func (s *Store) StartRun(ctx context.Context, run RunRecord) error {
	if run.ID == "" || run.ProjectID == "" || run.Provider == "" {
		return errors.New("run ID, project ID, and provider are required")
	}
	if run.State == "" {
		run.State = "RUNNING"
	}
	var workItem any
	if run.WorkItemID != "" {
		workItem = run.WorkItemID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE projects SET state='RUNNING' WHERE id=? AND state IN ('CREATED','READY','RESUMING')`, run.ProjectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("project is not runnable")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,project_id,work_item_id,provider,model,state,task_type,started_at) VALUES(?,?,?,?,?,?,?,?)`, run.ID, run.ProjectID, workItem, run.Provider, run.Model, run.State, run.TaskType, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishRun(ctx context.Context, runID, runState, projectState string) error {
	if runState != "VERIFYING" && runState != "FAILED" && runState != "DRAINING" && runState != "COMPLETED" {
		return fmt.Errorf("invalid terminal run state %q", runState)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE runs SET state=?,ended_at=? WHERE id=? AND state='RUNNING'`, runState, time.Now().UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("run is not active")
	}
	result, err = tx.ExecContext(ctx, `UPDATE projects SET state=? WHERE id=(SELECT project_id FROM runs WHERE id=?) AND state='RUNNING'`, projectState, runID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("project is not running")
	}
	if runState == "VERIFYING" {
		if _, err = tx.ExecContext(ctx, `UPDATE work_items SET status='VERIFYING' WHERE id=(SELECT work_item_id FROM runs WHERE id=?) AND status='IN_PROGRESS'`, runID); err != nil {
			return err
		}
	} else if runState == "FAILED" {
		if _, err = tx.ExecContext(ctx, `UPDATE work_items SET status='BACKLOG' WHERE id=(SELECT work_item_id FROM runs WHERE id=?) AND status='IN_PROGRESS'`, runID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) TransitionProjectState(ctx context.Context, projectID, from, to string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET state=? WHERE id=? AND state=?`, to, projectID, from)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("project state is not %s", from)
	}
	return nil
}

func (s *Store) RecordProviderEvent(ctx context.Context, projectID string, event provider.Event) error {
	return s.recordProviderEvent(ctx, projectID, event, true)
}

func (s *Store) RecordEphemeralProviderEvent(ctx context.Context, projectID string, event provider.Event) error {
	return s.recordProviderEvent(ctx, projectID, event, false)
}

func (s *Store) recordProviderEvent(ctx context.Context, projectID string, event provider.Event, persistSession bool) error {
	if event.RunID == "" || event.Type == "" || len(event.Raw) == 0 {
		return errors.New("event run ID, type, and raw payload are required")
	}
	var providerName, modelName string
	if err := s.db.QueryRowContext(ctx, `SELECT provider,model FROM runs WHERE id=? AND project_id=?`, event.RunID, projectID).Scan(&providerName, &modelName); err != nil {
		return err
	}
	eventKey := event.TurnID
	if eventKey == "" {
		eventKey = event.SessionID
	}
	if eventKey == "" {
		sum := sha256.Sum256(event.Raw)
		eventKey = fmt.Sprintf("%x", sum[:8])
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(event.Raw))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO event_logs(run_id,provider,event_type,event_key,raw_hash,raw_payload,created_at) VALUES(?,?,?,?,?,?,?)`, event.RunID, providerName, event.Type, eventKey, hash, audit.RedactBytes(event.Raw), now); err != nil {
		return err
	}
	if persistSession && event.SessionID != "" {
		retentionUntil := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(ctx, `UPDATE provider_session_history SET status='REPLACED',updated_at=?,retention_until=?,replacement_reason='provider issued a new session' WHERE project_id=? AND provider=? AND status='ACTIVE' AND session_id<>?`, now, retentionUntil, projectID, providerName, event.SessionID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO provider_session_history(project_id,provider,session_id,model,status,last_run_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(provider,session_id) DO UPDATE SET status='ACTIVE',last_run_id=excluded.last_run_id,updated_at=excluded.updated_at,expires_at=NULL,retention_until=NULL,replacement_reason=''`, projectID, providerName, event.SessionID, modelName, "ACTIVE", event.RunID, now, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO provider_sessions(project_id,provider,session_id,status,last_run_id,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,provider) DO UPDATE SET session_id=excluded.session_id,status='ACTIVE',last_run_id=excluded.last_run_id,updated_at=excluded.updated_at`, projectID, providerName, event.SessionID, "ACTIVE", event.RunID, now); err != nil {
			return err
		}
	}
	if event.Usage != nil {
		entries := []struct {
			name   string
			amount int64
			cost   float64
		}{{"input", event.Usage.InputTokens, 0}, {"output", event.Usage.OutputTokens, 0}, {"cached_input", event.Usage.CachedInputTokens, 0}, {"cache_creation", event.Usage.CacheCreationTokens, 0}, {"reasoning", event.Usage.ReasoningTokens, 0}, {"cost_usd", 0, event.Usage.CostUSD}}
		for _, entry := range entries {
			if entry.amount == 0 && entry.cost == 0 {
				continue
			}
			inserted, insertErr := tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_ledger(run_id,event_key,token_type,amount,cost,created_at) VALUES(?,?,?,?,?,?)`, event.RunID, eventKey, entry.name, entry.amount, entry.cost, now)
			if insertErr != nil {
				return insertErr
			}
			if (entry.name == "input" || entry.name == "output") && entry.amount > 0 {
				if rows, _ := inserted.RowsAffected(); rows == 1 {
					if _, err = tx.ExecContext(ctx, `UPDATE provider_session_history SET context_tokens_used=context_tokens_used+?,updated_at=?,last_run_id=? WHERE project_id=? AND provider=? AND status='ACTIVE'`, entry.amount, now, event.RunID, projectID, providerName); err != nil {
						return err
					}
				}
			}
		}
	}
	return tx.Commit()
}

func (s *Store) ActiveSession(ctx context.Context, projectID, providerName string) (SessionRecord, error) {
	var result SessionRecord
	var created, updated, expires, retention string
	err := s.db.QueryRowContext(ctx, `SELECT project_id,provider,session_id,model,status,last_run_id,context_tokens_used,created_at,updated_at,COALESCE(expires_at,''),COALESCE(retention_until,''),replacement_reason FROM provider_session_history WHERE project_id=? AND provider=? AND status='ACTIVE' AND (expires_at IS NULL OR expires_at>?)`, projectID, providerName, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&result.ProjectID, &result.Provider, &result.SessionID, &result.Model, &result.Status, &result.LastRunID, &result.ContextTokensUsed, &created, &updated, &expires, &retention, &result.ReplacementReason)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	result.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	result.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	result.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	result.RetentionUntil, _ = time.Parse(time.RFC3339Nano, retention)
	return result, err
}

func (s *Store) RunUsage(ctx context.Context, runID string) (provider.Usage, error) {
	var u provider.Usage
	rows, err := s.db.QueryContext(ctx, `SELECT token_type,SUM(amount),SUM(cost) FROM usage_ledger WHERE run_id=? GROUP BY token_type`, runID)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var amount int64
		var cost float64
		if err = rows.Scan(&kind, &amount, &cost); err != nil {
			return u, err
		}
		switch kind {
		case "input":
			u.InputTokens = amount
		case "output":
			u.OutputTokens = amount
		case "cached_input":
			u.CachedInputTokens = amount
		case "cache_creation":
			u.CacheCreationTokens = amount
		case "reasoning":
			u.ReasoningTokens = amount
		case "cost_usd":
			u.CostUSD = cost
		}
	}
	return u, rows.Err()
}

type ProjectBudget struct {
	TokenLimit, TokensUsed    int64
	CostLimitUSD, CostUsedUSD float64
	DailyRunLimit             int64
	DailyTokenLimit           int64
	DailyCostLimitUSD         float64
}

type DailyUsage struct {
	Runs, Tokens int64
	CostUSD      float64
	DayStart     time.Time
}

func (s *Store) SetProjectBudget(ctx context.Context, projectID string, tokenLimit int64, costLimit float64) error {
	if tokenLimit < 0 || costLimit < 0 {
		return errors.New("budget limits cannot be negative")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_budgets(project_id,token_limit,cost_limit_usd,updated_at) VALUES(?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET token_limit=excluded.token_limit,cost_limit_usd=excluded.cost_limit_usd,updated_at=excluded.updated_at`, projectID, tokenLimit, costLimit, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
func (s *Store) ProjectBudgetUsage(ctx context.Context, projectID string) (ProjectBudget, error) {
	var b ProjectBudget
	err := s.db.QueryRowContext(ctx, `SELECT b.token_limit,b.cost_limit_usd,COALESCE(SUM(CASE WHEN l.token_type<>'cost_usd' THEN l.amount ELSE 0 END),0),COALESCE(SUM(l.cost),0) FROM project_budgets b LEFT JOIN runs r ON r.project_id=b.project_id LEFT JOIN usage_ledger l ON l.run_id=r.id WHERE b.project_id=? GROUP BY b.project_id,b.token_limit,b.cost_limit_usd`, projectID).Scan(&b.TokenLimit, &b.CostLimitUSD, &b.TokensUsed, &b.CostUsedUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

func (s *Store) SetDailyLimits(ctx context.Context, projectID string, runLimit, tokenLimit int64, costLimit float64) error {
	if runLimit < 0 || tokenLimit < 0 || costLimit < 0 {
		return errors.New("daily limits cannot be negative")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_budgets(project_id,daily_run_limit,daily_token_limit,daily_cost_limit_usd,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET daily_run_limit=excluded.daily_run_limit,daily_token_limit=excluded.daily_token_limit,daily_cost_limit_usd=excluded.daily_cost_limit_usd,updated_at=excluded.updated_at`, projectID, runLimit, tokenLimit, costLimit, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ProjectDailyUsage(ctx context.Context, projectID string, now time.Time) (ProjectBudget, DailyUsage, error) {
	var budget ProjectBudget
	err := s.db.QueryRowContext(ctx, `SELECT daily_run_limit,daily_token_limit,daily_cost_limit_usd FROM project_budgets WHERE project_id=?`, projectID).Scan(&budget.DailyRunLimit, &budget.DailyTokenLimit, &budget.DailyCostLimitUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return budget, DailyUsage{}, ErrNotFound
	}
	if err != nil {
		return budget, DailyUsage{}, err
	}
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	usage := DailyUsage{DayStart: start}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE project_id=? AND started_at>=? AND started_at<?`, projectID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)).Scan(&usage.Runs); err != nil {
		return budget, usage, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN l.token_type<>'cost_usd' THEN l.amount ELSE 0 END),0),COALESCE(SUM(l.cost),0) FROM usage_ledger l JOIN runs r ON r.id=l.run_id WHERE r.project_id=? AND r.started_at>=? AND r.started_at<?`, projectID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)).Scan(&usage.Tokens, &usage.CostUSD); err != nil {
		return budget, usage, err
	}
	return budget, usage, nil
}

type QuotaWindow struct {
	Provider, AccountID, LimitType, Status, Source, Confidence, RawMessage string
	UsedPercent                                                            float64
	DetectedAt                                                             time.Time
	QuotaResetAt, ResumeAt                                                 *time.Time
}

func (s *Store) UpsertQuotaWindow(ctx context.Context, q QuotaWindow) error {
	if q.DetectedAt.IsZero() {
		q.DetectedAt = time.Now().UTC()
	}
	var reset, resume any
	if q.QuotaResetAt != nil {
		reset = q.QuotaResetAt.Format(time.RFC3339Nano)
	}
	if q.ResumeAt != nil {
		resume = q.ResumeAt.Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO quota_windows(provider,account_id,limit_type,status,used_percent,detected_at,quota_reset_at,resume_at,source,confidence,raw_message) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,account_id,limit_type) DO UPDATE SET status=excluded.status,used_percent=excluded.used_percent,detected_at=excluded.detected_at,quota_reset_at=excluded.quota_reset_at,resume_at=excluded.resume_at,source=excluded.source,confidence=excluded.confidence,raw_message=excluded.raw_message`, q.Provider, q.AccountID, q.LimitType, q.Status, q.UsedPercent, q.DetectedAt.Format(time.RFC3339Nano), reset, resume, q.Source, q.Confidence, audit.RedactString(q.RawMessage))
	return err
}
