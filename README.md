# GoalForge

GoalForge is a goal-first development orchestrator. The Go process owns project
state, versioned goals, work ordering, verification evidence, and completion;
AI sessions are execution tools rather than the source of truth.

## MVP commands

```sh
go run ./cmd/goalforge project init --name goalforge --repo . --provider codex
go run ./cmd/goalforge goal set --title "Build GoalForge" --objective "Ship the orchestrator MVP" \
  --criterion build_passed=true --criterion unit_test_pass_rate=100
go run ./cmd/goalforge goal show
go run ./cmd/goalforge milestone add --title "Persistence" --weight 2
go run ./cmd/goalforge work add --title "Implement session store" --priority 90 --weight 3 --estimated-tokens 12000 --scope "internal/session/**"
go run ./cmd/goalforge project provider set --provider claude --model sonnet --reason "compare provider quality"
go run ./cmd/goalforge project init --name demo --worktrees
go run ./cmd/goalforge rollback --work-item WORK-123 --reason "failed verification"
go run ./cmd/goalforge project budget --tokens 2000000 --cost-usd 100 --daily-runs 20 --daily-tokens 250000 --daily-cost-usd 15
go run ./cmd/goalforge project runtime --turn-timeout 30m --run-timeout 2h
go run ./cmd/goalforge serve --addr 127.0.0.1:8787
GOALFORGE_POSTGRES_DSN='postgres://goalforge:secret@localhost/goalforge' go run ./cmd/goalforge storage postgres migrate
go run ./cmd/goalforge work list
go run ./cmd/goalforge work status WORK-ID --set IN_PROGRESS
go run ./cmd/goalforge verify record --check build_passed --status PASSED --actual true
go run ./cmd/goalforge verify gate add --type build_passed \
  --command-json '["go","build","./..."]' --timeout-seconds 300
go run ./cmd/goalforge verify gate add --type unit_test_pass_rate \
  --command-json '["go","test","./..."]' --success-value 100
go run ./cmd/goalforge project budget --tokens 2000000 --cost-usd 100
go run ./cmd/goalforge ideas
go run ./cmd/goalforge continue
go run ./cmd/goalforge run --until-quota --max-runs 100
go run ./cmd/goalforge usage
go run ./cmd/goalforge sessions
go run ./cmd/goalforge checkpoint --next-action "implement the next approved work item"
go run ./cmd/goalforge logs --limit 50
go run ./cmd/goalforge pause
go run ./cmd/goalforge resume
go run ./cmd/goalforge cancel
GOALFORGE_CODEX_TRANSPORT=app-server go run ./cmd/goalforge worker
go run ./cmd/goalforge approval request --action protected-files --reason "rotate test certificate"
go run ./cmd/goalforge approval approve APPROVAL-ID
go run ./cmd/goalforge status
```

The database defaults to `.goalforge/goalforge.db`. Override it with
`--db PATH` or `GOALFORGE_DB`. Goal changes create a new immutable version and
require `--reason` after the first version.

`ideas` inspects the repository in a read-only, ephemeral provider session and
uses the providers' JSON-schema output mode. GoalForge scores and deduplicates
the returned candidates before persisting them; when the unimplemented backlog
has reached its policy limit, it refuses discovery and prioritizes implementation.

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
