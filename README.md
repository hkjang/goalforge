# GoalForge

GoalForge is a goal-first development orchestrator. The Go process owns project
state, versioned goals, work ordering, verification evidence, and completion;
AI sessions (Codex, Claude Code, Qwen Code, OpenCode) are execution tools
rather than the source of truth.

## Providers

| Provider | Binary | Transport | Read-only mapping | Writable mapping | Resume |
| --- | --- | --- | --- | --- | --- |
| `codex` | `codex` | `exec --json` (or App Server) | `--sandbox read-only` | `--sandbox workspace-write` | `exec resume ID` |
| `claude` | `claude` | `-p --output-format stream-json` | `--permission-mode plan` | `--permission-mode acceptEdits` | `--resume ID` |
| `qwen` | `qwen` | `--output-format stream-json` | `--approval-mode plan` | `--approval-mode auto-edit` | `--resume ID` |
| `opencode` | `opencode` | `run --format json` | `--agent plan` | `--auto` (denied permissions stay denied) | `--session ID` |

Writable mappings deliberately avoid each tool's broadest permission mode
(`--yolo`, `--dangerously-skip-permissions`); verification gates run under
GoalForge's own policy-checked engine either way. Run `goalforge doctor` to
verify the installed CLI supports every flag the adapter passes.

## Quick start

```sh
goalforge doctor                       # verify git, provider CLI, flags, auth
goalforge project init --name demo --provider claude --model haiku \
  --worktrees --auto-commit --fallback-model sonnet
goalforge goal set --title "Ship the feature" --objective "..." \
  --criterion build_passed=true
goalforge verify gate add --type build_passed --command-json '["go","build","./..."]'
goalforge work add --title "Implement session store" --priority 90 --scope "internal/session/**"
goalforge continue                     # one verified work item
goalforge status
```

## Command reference

### Setup and planning

```sh
goalforge doctor [--probe-auth]        # environment diagnostics before anything runs
goalforge project init --name N [--repo .] [--provider codex|claude|qwen|opencode] [--model M]
                       [--fallback-model M] [--worktrees] [--auto-commit]
goalforge project budget --tokens 2000000 --cost-usd 100 --daily-runs 20 --daily-tokens 250000 --daily-cost-usd 15
goalforge project runtime --turn-timeout 30m --run-timeout 2h
goalforge project provider set --provider claude --model sonnet --reason "..."
goalforge goal set --title T --objective O --criterion build_passed=true [--reason ...]
goalforge goal show
goalforge milestone add --title T --weight 2
goalforge work add --title T --priority 90 --weight 3 --estimated-tokens 12000 --scope "internal/session/**"
goalforge work list | work status ID --set APPROVED
goalforge verify gate add --type T --command-json '["go","test","./..."]' [--success-value 100]
```

### Discovery, execution, replanning

```sh
goalforge ideas                        # DISCOVER_IDEAS: read-only isolated discovery
goalforge audit                        # AUDIT_AND_IMPROVE: quality/security/perf/UX/ops inspection
goalforge replan                       # REPLAN_GOAL: gaps filed, stale backlog flagged for review
goalforge continue [--enqueue]         # CONTINUE_GOAL: one work item (or schedule for the worker)
goalforge develop                      # IMPLEMENT_SELECTED: highest-priority approved idea
goalforge run --until-quota --max-runs 100
goalforge worker [--once]              # processes RESUME and CONTINUE jobs, prunes sessions hourly
```

`ideas`/`audit`/`replan` run in read-only, ephemeral provider sessions using
JSON-schema output. Candidates are scored (`0.30 goal + 0.25 user + 0.20 ops +
0.15 feasibility + 0.10 risk-reduction`), trigram-deduplicated, and capped by
WIP policy; scope-expanding proposals are parked as `BLOCKED` for approval.
Work items without a manual token estimate get a conservative prediction from
recent run history so the 80%-quota large-work deferral always has a signal.

### Shipping verified work

Runs never push or merge on their own. With `--auto-commit`, a run whose gates
pass is committed in its worktree as author `GoalForge` with
`Goal-ID`/`Work-Item-ID`/`Run-ID` trailers, never on the default branch.

```sh
goalforge approval request --action merge-branch --reason "..."   # then: approval approve APR-...
goalforge merge --work-item WORK-1     # --no-ff into the default branch; conflicts abort for review
goalforge approval request --action publish-branch --reason "..."
goalforge publish --work-item WORK-1 [--remote origin]
goalforge worktree gc [--force]        # remove worktrees of DONE/DISCARDED items; branches kept
goalforge rollback --work-item WORK-1 --reason "..."
```

### Operations

```sh
goalforge status | usage | sessions | logs [--limit 50]
goalforge checkpoint --next-action "..."   # also writes continuity/<project>.md beside the DB
goalforge pause | resume | cancel
goalforge serve --addr 127.0.0.1:8787      # dashboard + JSON API + Prometheus /metrics
goalforge approval request --action protected-files|publish-branch|merge-branch --reason "..."
goalforge approval approve APR-ID
GOALFORGE_POSTGRES_DSN='postgres://...' goalforge storage postgres migrate
```

### Environment variables

| Variable | Purpose |
| --- | --- |
| `GOALFORGE_DB` | SQLite path (default `.goalforge/goalforge.db`; also `--db PATH`) |
| `GOALFORGE_CLAUDE_BIN` / `GOALFORGE_CODEX_BIN` / `GOALFORGE_QWEN_BIN` / `GOALFORGE_OPENCODE_BIN` | Provider CLI binary override |
| `GOALFORGE_WEBHOOK_URL` | Slack-compatible JSON webhook for WAITING_QUOTA / BLOCKED / COMPLETED |
| `GOALFORGE_AUDIT_KEY` | AES key (base64) to retain encrypted prompt originals |
| `GOALFORGE_CODEX_TRANSPORT=app-server` | Experimental Codex App Server transport |
| `GOALFORGE_CLAUDE_OTEL_ENDPOINT` / `_PROTOCOL` | Opt-in Claude OpenTelemetry export |

Goal changes create a new immutable version and require `--reason` after the
first version. Failed runs classify into a retry matrix (account quota waits
for reset without polling; short rate limits honor Retry-After; transient
failures back off 30s-1m-2m-5m-10m with jitter; auth and git conflicts block
for the user; an unsupported model switches to the approved fallback).

Operational commands expose the separate project token/cost ledger, provider
quota windows, persisted sessions, raw provider events, and Git-backed manual
checkpoints. `pause` and `cancel` persist control requests that a running
orchestrator polls; pause allows a drain grace period, interrupts the provider
process group, and records a recovery checkpoint. With no active AI execution,
`cancel` cancels pending scheduler jobs and refuses to change an already running
scheduler job only in the database.

## Audit and command security

GoalForge records each rendered prompt with its template name, SHA-256 hash,
and a redacted preview. Provider events, verification output, quota messages,
and checkpoint summaries are redacted before SQLite persistence. Set
`GOALFORGE_AUDIT_KEY` to a base64-encoded 16, 24, or 32 byte AES key to also
retain encrypted prompt originals using AES-GCM; without a key, plaintext
prompt originals are not stored.

Verification commands are executed as separated executable/argument arrays.
Destructive host and Git commands, shell command strings, and unapproved
network or remote-transfer executables are rejected when a gate is registered
and again immediately before execution.

Before writable AI runs, GoalForge hashes protected repository files such as
`.env`, private keys, keystores, and SSH configuration. An unapproved create,
change, or deletion blocks verification, returns the work item to the backlog,
sets the project to `BLOCKED`, and records a policy violation. Protected-file
approvals require an explicit request and approval and are consumed by one run.

`status` remains available after goal completion and reports weighted progress,
run success/failure and average duration, work-item outcomes, verification pass
rate, active session count, token categories, and accumulated cost.

## Codex App Server transport

The stable default remains `codex exec --json`. To use the deeper, experimental
Codex App Server integration for a command, set:

```sh
GOALFORGE_CODEX_TRANSPORT=app-server go run ./cmd/goalforge continue
```

GoalForge starts a local stdio App Server, performs the required
`initialize`/`initialized` handshake, and closes it after the command. The
transport uses `thread/start` or `thread/resume`, `turn/start`,
`thread/goal/set`, `thread/tokenUsage/updated`, `turn/interrupt`, and
`account/rateLimits/read`. It preserves exact Unix quota reset timestamps and
uses read-only or workspace-write sandbox policies with network access disabled.
The implementation contract is covered by adapter tests generated against the
installed Codex App Server experimental JSON schemas.

Before every provider call, GoalForge independently evaluates project token and
cost budgets plus provider quota. Warning usage is persisted; drain, block, or
exhausted usage with a known reset creates a Git/session checkpoint and one
idempotent `RESUME` job, then enters `WAITING_QUOTA` without calling the model.
An exhausted quota without a trustworthy reset enters `BLOCKED` instead of
polling. `worker` leases due jobs from SQLite, rechecks quota and repository
state, and resumes the saved session. Run it as a supervised long-lived process
or use `worker --once` from an external scheduler. `run --until-quota` executes
one verified work item at a time and stops at completion, quota wait, failure,
or its explicit consecutive-run cap.

## Claude StopFailure and OpenTelemetry

Claude runs install an execution-scoped `StopFailure` hook through an isolated
temporary settings file. The hook captures structured API failure type, details,
session ID, and rendered error without controlling retry behavior. Rate-limit
failures update the provider quota snapshot; reset timestamps, retry durations,
and clock times are parsed with explicit confidence. A runtime quota failure is
checkpointed and scheduled exactly like a Codex quota failure. If no reset can
be established, the project enters `BLOCKED`. After an estimated reset passes,
the worker permits one recheck attempt; another failure records a new window.

Structured stream results remain the authoritative GoalForge SQLite usage and
cost ledger. Optional Claude Code OpenTelemetry export is enabled only when an
operator sets `GOALFORGE_CLAUDE_OTEL_ENDPOINT`; GoalForge then configures OTLP
metrics and events with a run ID resource attribute while excluding account UUID
metric attributes. Set `GOALFORGE_CLAUDE_OTEL_PROTOCOL` to override the default
`http/protobuf` protocol. Prompt and tool contents remain disabled by default.
