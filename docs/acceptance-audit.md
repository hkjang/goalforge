# GoalForge acceptance audit

This document maps the original AC-001 through AC-020 requirements to current
authoritative evidence. `PASS` means a test exercises the required behavior;
`PARTIAL` and `MISSING` remain release blockers.

| AC | Status | Evidence / remaining gap |
|---|---|---|
| AC-001 | PASS | Goal version persistence and reopen test in `store_test.go`. |
| AC-002 | PASS | Codex CLI parser plus App Server thread/token tests. |
| AC-003 | PASS | Claude session, stream usage, StopFailure tests. |
| AC-004 | PASS | Persisted session is used by the next provider run. |
| AC-005 | PASS | Semantic/cycle duplicate idea tests and isolated discovery integration. |
| AC-006 | PASS | Planner claims one highest-priority approved/backlog item under WIP=1. |
| AC-007 | PASS | Work items persist token estimates; at 80% usage, runs above the 20k-token large-work threshold are deferred before any provider call. |
| AC-008 | PASS | Runtime quota failure preserves Git/session/usage checkpoint. |
| AC-009 | PASS | Exact reset creates one idempotent persistent resume job. |
| AC-010 | PASS | Worker rechecks quota and resumes the saved session. |
| AC-011 | PASS | Limited quota reschedules without an early due job. |
| AC-012 | PASS | Scheduler idempotency plus project lease tests. |
| AC-013 | PASS | Commit, branch, dirty-file names, and dirty content fingerprints are persisted and checked before resume. |
| AC-014 | PASS | Verification failure produces `REPAIR_REQUIRED`, never completion. |
| AC-015 | PASS | Token and cost budgets are evaluated independently before calls and resume. |
| AC-016 | PASS | A full close/reopen integration test rebuilds Store, Orchestrator, and Scheduler and resumes the persisted session exactly once. |
| AC-017 | PASS | Prompts, runs, commands, events, usage, and per-run file changes with content hashes are persisted for audit. |
| AC-018 | PASS | Dangerous commands are rejected, and a pre-run protected-file baseline restores every unapproved modification/deletion and removes newly created secret files before blocking the run. |
| AC-019 | PASS | Identical required-gate failures are fingerprinted across runs and block the project at the configured threshold. |
| AC-020 | PASS | Weighted work plus latest objective verification evidence gates completion. |

Additional release blockers from the numbered requirements:

- Provider switches create a neutral goal/checkpoint/backlog handoff, retire
  the old session, omit its ID, and start a new provider session. Projects can
  opt into persistent work-item branches and dedicated Git worktrees; writable
  runs on the configured default branch are isolated automatically.
- All writable tasks in registered Git repositories run under a single-writer
  lease in their dedicated worktree, so persisted run diffs are attributable
  to the AI execution. Recorded changes can be rolled back to the worktree base
  checkpoint without a broad `git clean`.
- UTC-day run, token, and cost caps are persisted separately from whole-project
  budgets. Project-specific provider Turn and orchestrator Run deadlines kill
  process groups and release failed work back to the backlog.
- Goal drift is checked from persisted per-run file-change evidence against the
  selected work item's required path scope before verification can run.
- A bearer-protectable local HTTP API and multi-project status dashboard are
  implemented. PostgreSQL now has versioned scheduler/lease migrations,
  `SKIP LOCKED` job claims, lease recovery, and a migration command; moving all
  authoritative project tables off SQLite remains operational work.
- GIT-009: projects can opt into `--auto-commit`; verified runs are committed
  with `Goal-ID`/`Work-Item-ID`/`Run-ID` trailers under a distinct GoalForge
  author, never on the protected default branch, and recorded in `run_commits`.
- Section 6.7: a per-error-type retry matrix (`policy.ClassifyFailure` /
  `DecideRetry`) with the 30s→1m→2m→5m→10m jittered backoff ladder drives
  `run --until-quota`; account-quota exhaustion never backs off, auth/git
  conflicts block for the user.
- Section 5.2: runs now persist a named task type (`DISCOVER_IDEAS`,
  `IMPLEMENT_SELECTED`, `CONTINUE_GOAL`, ...) in `runs.task_type`; `develop`
  and `continue` map to distinct types.
- LOOP-002/003/005: `same_work`, `same_change`, and `no_change` loop signals
  are wired from the verification path alongside `same_error`.
- The test suite and providers run on both Unix and Windows: process-group
  control is platform-split in `internal/procctl`, fake CLI fixtures come from
  `internal/testscript`, and the Claude StopFailure hook uses `sh`-portable
  paths.
- Section 5.2 `AUDIT_AND_IMPROVE`: `goalforge audit` runs a read-only isolated
  inspection across quality/security/performance/UX/operability and funnels
  candidates through the same dedup/scoring/WIP pipeline as idea discovery.
- Sections 6.5/18: every DB checkpoint (manual, safe drain, quota wait) also
  writes a human-readable `continuity/<project>.md` companion next to the
  database — outside the repository tree so the recorded dirty snapshot stays
  valid.
- Section 6.7 refinement: `policy.ParseRetryAfter` extracts Retry-After hints
  from provider error text so short rate-limit retries honor the provider's
  wait over the backoff ladder.
- Section 5.2 `REPLAN_GOAL`: `goalforge replan` compares implementation vs
  goal criteria; gap items flow through the discovery pipeline and stale
  BACKLOG/APPROVED entries are flagged `BLOCKED` for review — nothing is
  discarded automatically.
- Section 6.7 `model_unsupported`: projects carry an approved
  `--fallback-model`; `run --until-quota` switches to it via the provider
  handoff path (session retired, model change persisted) before retrying.
- LOOP-005 refinement: repeated no-change completion claims first rotate the
  provider session (`InvalidateSession`) and block only at twice the
  threshold; `RecoverFailedProject` returns FAILED projects and stuck work
  items to a runnable state so deliberate retries actually run.
- Worktree GC: `goalforge worktree gc [--force]` removes worktrees of
  DONE/DISCARDED work items (branches kept, dirty trees skipped unless
  forced) and marks them REMOVED in the store.
- Notifications: when `GOALFORGE_WEBHOOK_URL` is set, WAITING_QUOTA,
  BLOCKED (loop or pre-run), and COMPLETED transitions post a best-effort,
  secret-redacted, Slack-compatible JSON payload.
- Section 6.3 estimator: work items without a manual token estimate get a
  conservative prediction (recent work-run average + 50% margin via
  `EstimateWorkItemTokens`) so the 80%-quota large-work deferral always has
  a signal.
- Section 11 `turns`: provider turns are now first-class rows keyed by
  `(run_id, provider_turn_id)` with sticky terminal statuses, populated from
  the event stream alongside the usage ledger.
- Section 15: `/metrics` serves the observability metrics per project in
  Prometheus exposition format (bearer-protected when a token is set), with
  no external dependencies.
- SEC-011 publishing: `goalforge publish --work-item` pushes a verified work
  branch to a remote only after a consumable `PUBLISH_BRANCH` approval;
  runs never push on their own and only recorded verified commits qualify.
- `goalforge doctor` diagnoses the failure modes E2E surfaced before any
  run: git presence, provider CLI presence/version, adapter-flag support
  (would have caught the `--include-hook-events` bug), project
  registration, and optional `--probe-auth`.
- `goalforge merge --work-item` completes the pipeline: a verified work
  branch merges into the default branch behind a consumable `MERGE_BRANCH`
  approval; conflicts abort cleanly for user review (자동 병합 금지) and the
  merge commit carries Goal/Work/Run trailers.
- `goalforge usage` now reports ledger totals (per token type + cost)
  independently of whether a budget is configured.
- Autonomous continue: `goalforge continue --enqueue` schedules a persistent
  `CONTINUE` job; the worker executes one verified work item at a time,
  reschedules itself, waits out quota windows, and stops on completion or
  anything needing user judgment. `ScheduleRecurringJob` revives FAILED or
  COMPLETED jobs so the intent can be re-enqueued. The worker also prunes
  expired sessions hourly (SESSION-010).
- Claude hook capture registers `Stop` alongside `StopFailure`; non-failure
  hook lines are skipped instead of surfacing as decode errors, keeping the
  capture path useful on CLI versions without a StopFailure hook.
- CommitVerified stages first and commits only when `git diff --cached`
  shows real content, so Windows line-ending normalization can no longer
  fail a verified run with "nothing to commit".
- Mission-control dashboard: the embedded web UI now hash-routes to a
  project detail view — goal header with version/status, state chip with a
  live resume countdown, threshold-colored gauges (progress, token/cost
  budget, per-window account quota), completion-criteria checklist backed
  by `CriteriaStatus` evidence, backlog kanban, recent-runs table with task
  types and per-run tokens (`ListRecentRuns`), pending approvals with copy-
  ready CLI commands (`ListPendingApprovals`), and sessions. Completed
  projects fall back to their latest goal instead of "목표 미등록".
- Approval inbox: the dashboard gained its first write path. A cross-project
  pending-approvals panel (with project names) and per-approval 승인/거절
  buttons call POST approve/reject endpoints; mutations require the
  `X-Requested-With: GoalForge` header (CSRF preflight guard) on top of the
  bearer token, rejected approvals can never be consumed, and `goalforge
  approval reject` gives CLI parity. A stray `run approve` dispatch that
  aliased approval approve was removed.
- Idea triage board and quota timeline: the project detail view ranks
  BACKLOG/BLOCKED ideas by persisted priority score with the full scoring
  breakdown and scope-expansion flags, and 승인/보류/폐기 buttons call a new
  POST work-status endpoint restricted to triage transitions (execution
  states remain orchestrator-owned). A chronological timeline lists quota
  reset/resume times with source confidence alongside scheduled jobs.
- The manual sandbox E2E is codified as `cmd/goalforge/main_e2e_test.go`: an
  automated test drives the real CLI dispatch through project init → goal →
  work → gate → continue (worktree isolation, verification, auto-commit
  trailers, usage in status) → approval-denied/approved merge and publish →
  worktree gc → revived CONTINUE job via `worker --once`. The CLI layer —
  where both dispatch bugs hid — is no longer untested.
- E2E validated against the real Claude Code CLI (2.1.25) contract: the
  observed stream-json init/assistant/result payloads decode correctly
  (including `is_error:true` results), and a full
  init→goal→work→gate→continue→auto-commit→publish→gc pipeline passes with
  a contract-faithful fake CLI. Two real integration bugs were found and
  fixed: the CLI has no `--include-hook-events` flag, and `approval approve`
  was never dispatched. Provider binaries are now overridable via
  `GOALFORGE_CLAUDE_BIN` / `GOALFORGE_CODEX_BIN`.

The active goal must remain open until every required row is `PASS` or the
scope is explicitly revised by the user.
