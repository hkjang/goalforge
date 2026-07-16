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

The active goal must remain open until every required row is `PASS` or the
scope is explicitly revised by the user.
