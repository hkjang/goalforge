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
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/goalforge/goalforge/internal/audit"
	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/policy"
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
 estimated_tokens INTEGER NOT NULL DEFAULT 0 CHECK(estimated_tokens >= 0),
 objective TEXT NOT NULL DEFAULT '', acceptance TEXT NOT NULL DEFAULT '',
 blocked_reason TEXT NOT NULL DEFAULT '', owner TEXT NOT NULL DEFAULT 'AI'
);
CREATE TABLE IF NOT EXISTS design_decisions (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), goal_id TEXT NOT NULL DEFAULT '',
 work_item_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL, context TEXT NOT NULL DEFAULT '',
 decision TEXT NOT NULL, alternatives TEXT NOT NULL DEFAULT '', consequences TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'ACCEPTED', superseded_by TEXT NOT NULL DEFAULT '',
 base_commit TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_decisions_project ON design_decisions(project_id, status);
CREATE TABLE IF NOT EXISTS integration_checks (
 project_id TEXT PRIMARY KEY REFERENCES projects(id), pending INTEGER NOT NULL DEFAULT 0,
 reason TEXT NOT NULL DEFAULT '', target_sha TEXT NOT NULL DEFAULT '',
 last_passed INTEGER NOT NULL DEFAULT 0, last_sha TEXT NOT NULL DEFAULT '',
 last_details TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS evaluation_cases (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), name TEXT NOT NULL, kind TEXT NOT NULL,
 repository TEXT NOT NULL DEFAULT '', goal_title TEXT NOT NULL DEFAULT '', goal_objective TEXT NOT NULL DEFAULT '',
 notes TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, UNIQUE(project_id,name)
);
CREATE TABLE IF NOT EXISTS evaluation_results (
 id TEXT PRIMARY KEY, case_id TEXT NOT NULL REFERENCES evaluation_cases(id), label TEXT NOT NULL,
 run_id TEXT NOT NULL, project_id TEXT NOT NULL, provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
 config_version TEXT NOT NULL DEFAULT '', passed INTEGER NOT NULL DEFAULT 0, tokens INTEGER NOT NULL DEFAULT 0,
 cost_usd REAL NOT NULL DEFAULT 0, interventions INTEGER NOT NULL DEFAULT 0, duration_seconds REAL NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS goal_contracts (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), goal_id TEXT NOT NULL DEFAULT '',
 version INTEGER NOT NULL, previous_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL,
 objective TEXT NOT NULL DEFAULT '', users TEXT NOT NULL DEFAULT '', scenarios TEXT NOT NULL DEFAULT '',
 exclusions TEXT NOT NULL DEFAULT '', stage TEXT NOT NULL DEFAULT '', budget_tokens INTEGER NOT NULL DEFAULT 0,
 budget_usd REAL NOT NULL DEFAULT 0, deadline TEXT NOT NULL DEFAULT '', change_reason TEXT NOT NULL DEFAULT '',
 decider TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, UNIQUE(project_id,version)
);
CREATE TABLE IF NOT EXISTS contract_outcomes (
 contract_id TEXT NOT NULL REFERENCES goal_contracts(id), outcome_key TEXT NOT NULL,
 statement TEXT NOT NULL DEFAULT '', method TEXT NOT NULL DEFAULT '', judge TEXT NOT NULL DEFAULT '',
 metric TEXT NOT NULL DEFAULT '', comparator TEXT NOT NULL DEFAULT '', threshold TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'UNCONFIRMED', PRIMARY KEY(contract_id,outcome_key)
);
CREATE TABLE IF NOT EXISTS outbox (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), kind TEXT NOT NULL,
 payload TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, published_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_outbox_pending ON outbox(published_at, created_at);
CREATE TABLE IF NOT EXISTS external_effects (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), run_id TEXT NOT NULL DEFAULT '',
 work_item_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, effect_key TEXT NOT NULL UNIQUE,
 target TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', request_hash TEXT NOT NULL DEFAULT '', state TEXT NOT NULL,
 result TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS evaluation_trials (
 id TEXT PRIMARY KEY, case_id TEXT NOT NULL REFERENCES evaluation_cases(id), label TEXT NOT NULL,
 repetition INTEGER NOT NULL DEFAULT 1, condition_hash TEXT NOT NULL DEFAULT '',
 clean_tree_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '',
 completed INTEGER NOT NULL DEFAULT 0, tokens INTEGER NOT NULL DEFAULT 0, cost_usd REAL NOT NULL DEFAULT 0,
 interventions INTEGER NOT NULL DEFAULT 0, duration_seconds REAL NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS takeovers (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), work_item_id TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '', workspace TEXT NOT NULL DEFAULT '', stopped_run_id TEXT NOT NULL DEFAULT '',
 taken_at TEXT NOT NULL, returned_at TEXT NOT NULL DEFAULT '', return_summary TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS work_item_dependencies (
 work_item_id TEXT NOT NULL REFERENCES work_items(id), depends_on_id TEXT NOT NULL REFERENCES work_items(id),
 PRIMARY KEY(work_item_id,depends_on_id)
);
CREATE TABLE IF NOT EXISTS verification_results (
 id INTEGER PRIMARY KEY AUTOINCREMENT, goal_id TEXT NOT NULL REFERENCES goals(id), run_id TEXT,
 check_type TEXT NOT NULL, status TEXT NOT NULL, actual_value TEXT NOT NULL DEFAULT '',
 command TEXT NOT NULL DEFAULT '', exit_code INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0,
 required INTEGER NOT NULL DEFAULT 1, output TEXT NOT NULL DEFAULT '',
 failure_kind TEXT NOT NULL DEFAULT '', repair_mode TEXT NOT NULL DEFAULT '',
 stale INTEGER NOT NULL DEFAULT 0, stale_reason TEXT NOT NULL DEFAULT '',
 tree_id TEXT NOT NULL DEFAULT '', evaluator_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS verification_relaxations (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), run_id TEXT NOT NULL DEFAULT '',
 kind TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', before_value TEXT NOT NULL DEFAULT '',
 after_value TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS repair_attempts (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), work_item_id TEXT NOT NULL,
 run_id TEXT NOT NULL, failure_kind TEXT NOT NULL, repair_mode TEXT NOT NULL, decision TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '', attempt INTEGER NOT NULL DEFAULT 1, cost_usd REAL NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL
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
CREATE TABLE IF NOT EXISTS turns (
 run_id TEXT NOT NULL REFERENCES runs(id), provider_turn_id TEXT NOT NULL,
 status TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(run_id, provider_turn_id)
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
 requested_at TEXT NOT NULL, approved_at TEXT, consumed_run_id TEXT,
 work_item_id TEXT NOT NULL DEFAULT '', source_branch TEXT NOT NULL DEFAULT '',
 rejection_category TEXT NOT NULL DEFAULT '', rejection_note TEXT NOT NULL DEFAULT '',
 target_ref TEXT NOT NULL DEFAULT '', commit_sha TEXT NOT NULL DEFAULT '',
 files_changed INTEGER NOT NULL DEFAULT 0
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
 heartbeat_at TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1
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
 success_value TEXT NOT NULL DEFAULT 'true', value_pattern TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,check_type)
);
CREATE TABLE IF NOT EXISTS audit_chain (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, record_id TEXT NOT NULL,
 payload_digest TEXT NOT NULL, prev_digest TEXT NOT NULL DEFAULT '', digest TEXT NOT NULL,
 recorded_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_chain_record ON audit_chain(kind, record_id);
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
	if err := s.ensureColumn(ctx, "runs", "config_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "projects", "fallback_model", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "projects", "wip_limit", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "verification_gates", "value_pattern", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// A gate declares what kind of evidence it produces and a criterion
	// declares what kind it needs. Both default to empty, which is what every
	// gate and criterion written before kinds existed means: unclassified.
	if err := s.ensureColumn(ctx, "verification_gates", "kind", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "goal_criteria", "required_kind", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Trials recorded before arms existed were all GoalForge's own.
	if err := s.ensureColumn(ctx, "evaluation_trials", "arm", "TEXT NOT NULL DEFAULT 'goalforge'"); err != nil {
		return err
	}
	// Decisions recorded before scopes existed are about the project as a
	// whole, which is what an empty scope means here.
	if err := s.ensureColumn(ctx, "design_decisions", "scope", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// The state a run started from. Without it a run cannot be reproduced: the
	// only commit recorded was the one the run produced, which a failed run
	// never has, so "reproduce" could describe a run but never re-run it.
	if err := s.ensureColumn(ctx, "runs", "base_commit", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// The version a board read, so an edit made against a stale view is
	// refused instead of overwriting whatever happened in between.
	if err := s.ensureColumn(ctx, "work_items", "version", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "verification_results", "evidence_kind", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "process_leases", "generation", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	for _, column := range []struct{ name, definition string }{{"sandbox_mode", "TEXT NOT NULL DEFAULT 'none'"}, {"sandbox_image", "TEXT NOT NULL DEFAULT ''"}, {"sandbox_memory_mb", "INTEGER NOT NULL DEFAULT 2048"}, {"sandbox_cpus", "REAL NOT NULL DEFAULT 2"}, {"sandbox_processes", "INTEGER NOT NULL DEFAULT 256"}, {"sandbox_network", "INTEGER NOT NULL DEFAULT 0"}} {
		if err := s.ensureColumn(ctx, "projects", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range []string{"failure_kind", "repair_mode", "stale_reason", "tree_id", "evaluator_id"} {
		if err := s.ensureColumn(ctx, "verification_results", column, "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "verification_results", "stale", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	for _, column := range []struct{ name, definition string }{{"fixture", "TEXT NOT NULL DEFAULT ''"}, {"fixture_ref", "TEXT NOT NULL DEFAULT ''"}, {"clean_tree_id", "TEXT NOT NULL DEFAULT ''"}, {"criteria_json", "TEXT NOT NULL DEFAULT ''"}, {"gates_json", "TEXT NOT NULL DEFAULT ''"}, {"token_budget", "INTEGER NOT NULL DEFAULT 0"}, {"cost_budget_usd", "REAL NOT NULL DEFAULT 0"}, {"timeout_seconds", "INTEGER NOT NULL DEFAULT 0"}} {
		if err := s.ensureColumn(ctx, "evaluation_cases", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range []struct{ name, definition string }{{"objective", "TEXT NOT NULL DEFAULT ''"}, {"acceptance", "TEXT NOT NULL DEFAULT ''"}, {"blocked_reason", "TEXT NOT NULL DEFAULT ''"}, {"owner", "TEXT NOT NULL DEFAULT 'AI'"}} {
		if err := s.ensureColumn(ctx, "work_items", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range []struct{ name, definition string }{{"rejection_category", "TEXT NOT NULL DEFAULT ''"}, {"rejection_note", "TEXT NOT NULL DEFAULT ''"}, {"work_item_id", "TEXT NOT NULL DEFAULT ''"}, {"source_branch", "TEXT NOT NULL DEFAULT ''"}, {"target_ref", "TEXT NOT NULL DEFAULT ''"}, {"commit_sha", "TEXT NOT NULL DEFAULT ''"}, {"files_changed", "INTEGER NOT NULL DEFAULT 0"}} {
		if err := s.ensureColumn(ctx, "approvals", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range []struct{ name, definition string }{{"daily_run_limit", "INTEGER NOT NULL DEFAULT 0"}, {"daily_token_limit", "INTEGER NOT NULL DEFAULT 0"}, {"daily_cost_limit_usd", "REAL NOT NULL DEFAULT 0"}} {
		if err := s.ensureColumn(ctx, "project_budgets", column.name, column.definition); err != nil {
			return err
		}
	}
	// Single-predecessor rows predate the dependency table; carry them over
	// so existing plans keep their ordering.
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO work_item_dependencies(work_item_id,depends_on_id) SELECT id,dependency FROM work_items WHERE dependency<>'' AND EXISTS(SELECT 1 FROM work_items d WHERE d.id=work_items.dependency)`); err != nil {
		return err
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

var lastGeneratedID atomic.Int64

// NewID returns a time-ordered identifier that stays unique even when the
// platform clock is coarser than a nanosecond (Windows returns identical
// UnixNano values for rapid consecutive calls, which produced intermittent
// UNIQUE violations).
func NewID(prefix string) string {
	for {
		now := time.Now().UTC().UnixNano()
		last := lastGeneratedID.Load()
		if now <= last {
			now = last + 1
		}
		if lastGeneratedID.CompareAndSwap(last, now) {
			return fmt.Sprintf("%s-%d", prefix, now)
		}
	}
}

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
	if p.WIPLimit <= 0 {
		p.WIPLimit = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects(id,name,repository_path,default_branch,provider,model,fallback_model,state,worktree_enabled,auto_commit_enabled,wip_limit,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.RepositoryPath, p.DefaultBranch, p.Provider, p.Model, p.FallbackModel, p.State, p.WorktreeEnabled, p.AutoCommitEnabled, p.WIPLimit, p.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) ProjectByPath(ctx context.Context, path string) (model.Project, error) {
	path, _ = filepath.Abs(path)
	var p model.Project
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,repository_path,default_branch,provider,model,fallback_model,state,worktree_enabled,auto_commit_enabled,COALESCE(wip_limit,1),created_at FROM projects WHERE repository_path=?`, path).
		Scan(&p.ID, &p.Name, &p.RepositoryPath, &p.DefaultBranch, &p.Provider, &p.Model, &p.FallbackModel, &p.State, &p.WorktreeEnabled, &p.AutoCommitEnabled, &p.WIPLimit, &created)
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
	err := s.db.QueryRowContext(ctx, `SELECT id,name,repository_path,default_branch,provider,model,fallback_model,state,worktree_enabled,auto_commit_enabled,COALESCE(wip_limit,1),created_at FROM projects WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.RepositoryPath, &p.DefaultBranch, &p.Provider, &p.Model, &p.FallbackModel, &p.State, &p.WorktreeEnabled, &p.AutoCommitEnabled, &p.WIPLimit, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err == nil {
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	}
	return p, err
}

func (s *Store) ListProjects(ctx context.Context) ([]model.Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,repository_path,default_branch,provider,model,fallback_model,state,worktree_enabled,auto_commit_enabled,COALESCE(wip_limit,1),created_at FROM projects ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []model.Project
	for rows.Next() {
		var project model.Project
		var created string
		if err = rows.Scan(&project.ID, &project.Name, &project.RepositoryPath, &project.DefaultBranch, &project.Provider, &project.Model, &project.FallbackModel, &project.State, &project.WorktreeEnabled, &project.AutoCommitEnabled, &project.WIPLimit, &created); err != nil {
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
		if kindErr := policy.ValidGateKind(c.RequiredKind); kindErr != nil {
			return g, fmt.Errorf("criterion %s: %w", c.Type, kindErr)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO goal_criteria(goal_id,criterion_type,expected_value,required_kind) VALUES(?,?,?,?)`, g.ID, c.Type, c.ExpectedValue, strings.ToLower(strings.TrimSpace(c.RequiredKind))); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT criterion_type,expected_value,COALESCE(required_kind,'') FROM goal_criteria WHERE goal_id=? ORDER BY criterion_type`, g.ID)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var c model.Criterion
		if err = rows.Scan(&c.Type, &c.ExpectedValue, &c.RequiredKind); err != nil {
			return g, err
		}
		g.Criteria = append(g.Criteria, c)
	}
	return g, rows.Err()
}

// ProgressDetail explains a goal's progress instead of reducing it to one
// number: which weight is in scope, which was discarded out of scope, and
// which completion criteria are actually backed by evidence.
type ProgressDetail struct {
	Percent                                  float64
	Complete                                 bool
	TotalWeight, DoneWeight, DiscardedWeight float64
	TotalItems, DoneItems, DiscardedItems    int
	Criteria                                 []CriterionStatus
	CriteriaMet                              bool
	// UnconfirmedOutcomes and OutcomeConflicts are the contract's own reasons
	// the goal is not finished.
	//
	// The contract states what the goal requires. An outcome nobody has agreed
	// how to settle is a requirement that cannot be met, and two that cannot
	// both hold mean the goal has no achievable definition — in either case
	// the work being done says nothing. These were reported by the plan
	// preview as advice and ignored by the verdict, so a goal could be marked
	// complete against a contract that could not be satisfied.
	UnconfirmedOutcomes []string
	OutcomeConflicts    []string
	// IncompleteReason is the first thing standing between here and done,
	// stated once so every surface says the same thing.
	IncompleteReason string
}

// GoalProgress reports percent complete and whether the goal is finished.
func (s *Store) GoalProgress(ctx context.Context, goal model.Goal) (float64, bool, error) {
	detail, err := s.GoalProgressDetail(ctx, goal)
	return detail.Percent, detail.Complete, err
}

// GoalProgressDetail computes progress over the goal's in-scope work only.
// DISCARDED items are removed from the baseline rather than counted as
// outstanding: leaving them in the denominator made `done == total`
// unreachable, so a single discarded item blocked goal completion forever.
// Required completion criteria still have to be met with evidence.
func (s *Store) GoalProgressDetail(ctx context.Context, goal model.Goal) (ProgressDetail, error) {
	var detail ProgressDetail
	if err := s.db.QueryRowContext(ctx, `SELECT
COALESCE(SUM(CASE WHEN status<>'DISCARDED' THEN weight ELSE 0 END),0),
COALESCE(SUM(CASE WHEN status='DONE' THEN weight ELSE 0 END),0),
COALESCE(SUM(CASE WHEN status='DISCARDED' THEN weight ELSE 0 END),0),
COALESCE(SUM(CASE WHEN status<>'DISCARDED' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN status='DONE' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN status='DISCARDED' THEN 1 ELSE 0 END),0)
FROM work_items WHERE goal_id=?`, goal.ID).Scan(&detail.TotalWeight, &detail.DoneWeight, &detail.DiscardedWeight,
		&detail.TotalItems, &detail.DoneItems, &detail.DiscardedItems); err != nil {
		return detail, err
	}
	criteria, err := s.CriteriaStatus(ctx, goal)
	if err != nil {
		return detail, err
	}
	detail.Criteria = criteria
	detail.CriteriaMet = len(criteria) > 0
	for _, c := range criteria {
		if !c.Satisfied {
			detail.CriteriaMet = false
		}
	}
	if detail.TotalWeight > 0 {
		detail.Percent = detail.DoneWeight / detail.TotalWeight * 100
	}
	detail.Complete = detail.CriteriaMet && detail.TotalWeight > 0 && detail.DoneWeight == detail.TotalWeight
	if err = s.applyContractToCompletion(ctx, goal.ProjectID, &detail); err != nil {
		return detail, err
	}
	detail.IncompleteReason = incompleteReason(detail)
	return detail, nil
}

// applyContractToCompletion lets the contract have its say.
//
// A missing contract is not a reason to be incomplete: contracts are optional
// and a project without one is judged exactly as it was before.
func (s *Store) applyContractToCompletion(ctx context.Context, projectID string, detail *ProgressDetail) error {
	if projectID == "" {
		return nil
	}
	contract, err := s.CurrentContract(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, outcome := range contract.Unconfirmed() {
		detail.UnconfirmedOutcomes = append(detail.UnconfirmedOutcomes, outcome.Key)
	}
	for _, conflict := range contract.Conflicts() {
		detail.OutcomeConflicts = append(detail.OutcomeConflicts,
			fmt.Sprintf("%s vs %s: %s", conflict.Left.Key, conflict.Right.Key, conflict.Detail))
	}
	if len(detail.UnconfirmedOutcomes) > 0 || len(detail.OutcomeConflicts) > 0 {
		detail.Complete = false
	}
	return nil
}

// incompleteReason names the first thing standing between here and done, so
// every surface gives the same answer instead of each deriving its own.
func incompleteReason(detail ProgressDetail) string {
	switch {
	case len(detail.OutcomeConflicts) > 0:
		return "계약의 필수 결과가 서로 충돌합니다: " + detail.OutcomeConflicts[0]
	case len(detail.UnconfirmedOutcomes) > 0:
		return "판정 방법이나 주체가 정해지지 않은 필수 결과: " + strings.Join(detail.UnconfirmedOutcomes, ", ")
	}
	for _, criterion := range detail.Criteria {
		if !criterion.Satisfied {
			return fmt.Sprintf("완료 조건 %s 미충족 (%s)", criterion.Type, criterion.Status)
		}
	}
	switch {
	case detail.TotalWeight == 0:
		return "작업이 없습니다"
	case detail.DoneWeight < detail.TotalWeight:
		return fmt.Sprintf("조건은 모두 충족되었으나 작업이 %.0f%% 진행되었습니다", detail.Percent)
	default:
		return ""
	}
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return w, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_items(id,goal_id,milestone_id,type,title,priority,status,risk,change_scope,weight,estimated_tokens,objective,acceptance) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, w.ID, w.GoalID, milestone, w.Type, w.Title, w.Priority, w.Status, w.Risk, w.ChangeScope, w.Weight, w.EstimatedTokens, w.Objective, w.Acceptance); err != nil {
		return w, err
	}
	if err = s.setDependencies(ctx, tx, w.GoalID, w.ID, w.Dependencies); err != nil {
		return w, err
	}
	return w, tx.Commit()
}

func (s *Store) ListWorkItems(ctx context.Context, goalID string) ([]model.WorkItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,goal_id,COALESCE(milestone_id,''),type,title,priority,status,risk,change_scope,weight,estimated_tokens,objective,acceptance,blocked_reason FROM work_items WHERE goal_id=? ORDER BY CASE status WHEN 'IN_PROGRESS' THEN 0 WHEN 'BACKLOG' THEN 1 ELSE 2 END, priority DESC, id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.WorkItem
	for rows.Next() {
		var w model.WorkItem
		if err := rows.Scan(&w.ID, &w.GoalID, &w.MilestoneID, &w.Type, &w.Title, &w.Priority, &w.Status, &w.Risk, &w.ChangeScope, &w.Weight, &w.EstimatedTokens, &w.Objective, &w.Acceptance, &w.BlockedReason); err != nil {
			return nil, err
		}
		result = append(result, w)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	dependencies, err := s.dependencyRows(ctx, goalID)
	if err != nil {
		return nil, err
	}
	for i := range result {
		result[i].Dependencies = dependencies[result[i].ID]
	}
	return result, nil
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
	if err = s.setWorkItemStatusTx(ctx, tx, goalID, workID, status); err != nil {
		return err
	}
	return tx.Commit()
}

// setWorkItemStatusTx applies the change inside a caller's transaction, so a
// manual transition can check the item's version and change its status without
// a window between the two where somebody else's edit lands.
func (s *Store) setWorkItemStatusTx(ctx context.Context, tx *sql.Tx, goalID, workID, status string) error {
	var exists string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM work_items WHERE id=? AND goal_id=?`, workID, goalID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if status == "IN_PROGRESS" {
		if err := s.checkConcurrency(ctx, tx, goalID, workID); err != nil {
			return err
		}
		unmet, unmetErr := s.unmetDependencies(ctx, tx, workID)
		if unmetErr != nil {
			return unmetErr
		}
		if len(unmet) > 0 {
			return fmt.Errorf("dependencies not done: %s", strings.Join(unmet, ", "))
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET status=? WHERE id=? AND goal_id=?`, status, workID, goalID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RecordVerification(ctx context.Context, goalID, checkType, status, actual, output string) error {
	if status != "PASSED" && status != "FAILED" && status != "UNKNOWN" {
		return fmt.Errorf("invalid verification status %q", status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := chainStamp(time.Now())
	inserted, err := tx.ExecContext(ctx, `INSERT INTO verification_results(goal_id,check_type,status,actual_value,output,created_at) VALUES(?,?,?,?,?,?)`, goalID, checkType, status, actual, audit.RedactString(output), now)
	if err != nil {
		return err
	}
	id, err := inserted.LastInsertId()
	if err != nil {
		return err
	}
	if err = appendChain(ctx, tx, ChainEvidence, fmt.Sprint(id),
		evidenceDigest(goalID, "", checkType, status, actual, "", now, audit.RedactString(output), 1), now); err != nil {
		return err
	}
	return tx.Commit()
}

// CriterionMet is exported so a baseline arm is judged by the identical
// comparison GoalForge uses. A comparison whose two sides apply different
// thresholds measures the judge, not the systems.
func CriterionMet(expected, actual string) bool { return criterionMet(expected, actual) }

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
	// BaseCommit is the commit the workspace was on when the run started. It
	// is what makes the run reproducible; the commit a run *produces* is a
	// different thing and a failed run does not have one.
	BaseCommit string
	// ConfigVersion identifies the configuration this run executed under, so
	// a later change in success rate or cost can be attributed rather than
	// guessed at.
	ConfigVersion string
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
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,project_id,work_item_id,provider,model,state,task_type,config_version,base_commit,started_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, run.ID, run.ProjectID, workItem, run.Provider, run.Model, run.State, run.TaskType, run.ConfigVersion, run.BaseCommit, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
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
	if event.TurnID != "" {
		turnStatus := "RUNNING"
		switch event.Type {
		case provider.EventCompleted:
			turnStatus = "COMPLETED"
		case provider.EventFailed:
			turnStatus = "FAILED"
		}
		// Terminal turn statuses stick; later stream events never downgrade
		// a COMPLETED or FAILED turn back to RUNNING.
		if _, err = tx.ExecContext(ctx, `INSERT INTO turns(run_id,provider_turn_id,status,created_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(run_id,provider_turn_id) DO UPDATE SET status=CASE WHEN turns.status='RUNNING' THEN excluded.status ELSE turns.status END,updated_at=excluded.updated_at`, event.RunID, event.TurnID, turnStatus, now, now); err != nil {
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

// TurnRecord is one provider turn inside a run (requirement 11: first-class
// turns instead of folding them into usage event keys).
type TurnRecord struct {
	RunID, ProviderTurnID, Status string
	CreatedAt, UpdatedAt          time.Time
}

func (s *Store) ListTurns(ctx context.Context, runID string) ([]TurnRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,provider_turn_id,status,created_at,updated_at FROM turns WHERE run_id=? ORDER BY created_at,provider_turn_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []TurnRecord
	for rows.Next() {
		var turn TurnRecord
		var created, updated string
		if err = rows.Scan(&turn.RunID, &turn.ProviderTurnID, &turn.Status, &created, &updated); err != nil {
			return nil, err
		}
		turn.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		turn.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		turns = append(turns, turn)
	}
	return turns, rows.Err()
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

// ProjectBudgetConfig reads every configured limit without usage. Editing one
// limit needs the rest of them, and the usage query returns only the two
// totals it joins over.
func (s *Store) ProjectBudgetConfig(ctx context.Context, projectID string) (ProjectBudget, error) {
	var b ProjectBudget
	err := s.db.QueryRowContext(ctx, `SELECT token_limit,cost_limit_usd,daily_run_limit,daily_token_limit,daily_cost_limit_usd FROM project_budgets WHERE project_id=?`, projectID).
		Scan(&b.TokenLimit, &b.CostLimitUSD, &b.DailyRunLimit, &b.DailyTokenLimit, &b.DailyCostLimitUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
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

// projectName resolves a project's display name for notifications, falling
// back to the ID so a lookup failure never blocks the notification itself.
func (s *Store) projectName(ctx context.Context, projectID string) string {
	var name string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM projects WHERE id=?`, projectID).Scan(&name); err != nil || name == "" {
		return projectID
	}
	return name
}

// RelocateProject points a registered project at a new repository path.
//
// The path is how a project is found, so a repository that moves becomes
// unreachable: every command reports "no project here" while the state — the
// goal, the evidence, the approvals — is still in the database. Rewriting the
// path is the whole recovery, and it is a rename rather than a re-registration
// because re-registering would start a second project beside the first and
// leave the history behind.
func (s *Store) RelocateProject(ctx context.Context, projectID, newPath string) (model.Project, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(newPath))
	if err != nil {
		return model.Project{}, err
	}
	if info, statErr := os.Stat(absolute); statErr != nil || !info.IsDir() {
		return model.Project{}, fmt.Errorf("%s 는 존재하는 디렉터리가 아닙니다", absolute)
	}
	// Another project already living there would mean two records pointing at
	// one repository, and every later lookup answering arbitrarily.
	if existing, pathErr := s.ProjectByPath(ctx, absolute); pathErr == nil && existing.ID != projectID {
		return model.Project{}, fmt.Errorf("%s 에는 이미 프로젝트 %s 가 등록되어 있습니다", absolute, existing.Name)
	} else if pathErr != nil && !errors.Is(pathErr, ErrNotFound) {
		return model.Project{}, pathErr
	}
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET repository_path=? WHERE id=?`, absolute, projectID)
	if err != nil {
		return model.Project{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return model.Project{}, ErrNotFound
	}
	return s.ProjectByID(ctx, projectID)
}
