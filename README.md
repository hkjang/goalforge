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
goalforge project concurrency --wip 2      # only items with disjoint change scopes run together
goalforge project profile personal|team|production   # an operating posture as one set of limits
goalforge project provider set --provider claude --model sonnet --reason "..."
goalforge goal set --title T --objective O --criterion build_passed=true [--reason ...]
goalforge goal show
goalforge milestone add --title T --weight 2
goalforge work add --title T --priority 90 --weight 3 --estimated-tokens 12000 --scope "internal/session/**"
                   [--depends-on WORK-1,WORK-2]   # every predecessor must be DONE first
goalforge work list | work status ID --set APPROVED
goalforge verify template go-api|node-frontend|python-library|docs [--overwrite]
goalforge verify gate add --type T --command-json '["go","test","./..."]' [--success-value 100]
                          [--value-pattern 'coverage:\s+([0-9.]+)%']   # measure, do not assume
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
goalforge approval request --action merge-branch --work-item WORK-1 --reason "..."
goalforge merge --work-item WORK-1     # --no-ff into the default branch; conflicts abort for review
goalforge approval request --action publish-branch --work-item WORK-1 [--remote origin] --reason "..."
goalforge publish --work-item WORK-1 [--remote origin]
goalforge worktree gc [--force]        # remove worktrees of DONE/DISCARDED items; branches kept
goalforge rollback --work-item WORK-1 --reason "..."
```

### Operations

```sh
goalforge status | usage | sessions | logs [--limit 50]
goalforge report [--since 24h] [--json]    # what ran, what stopped and why, what awaits you
goalforge models [--task-type CONTINUE_GOAL]  # model records, the next choice and why, cost forecast
goalforge verify integration               # verify the merged result on the default branch
goalforge decision add --title T --decision "..." [--alternatives "..."] [--consequences "..."] [--supersedes DEC-1]
goalforge decision list [--all]            # settled architecture, inherited by every later session
goalforge reproduce --run RUN-1 [--out ./repro]   # commit, workspace, gate commands, logs, environment
goalforge takeover --work-item WORK-1 --reason "..."        # stop automation and take the workspace
goalforge takeover return --work-item WORK-1 --summary "..." # re-verify and hand it back
goalforge eval add --name N --kind bug_fix|feature|refactor|docs --objective "..."
goalforge eval record --case EVAL-1 --label "haiku+v2" --run RUN-1
goalforge eval compare [--case EVAL-1]     # pass rate, cost per run, manual interventions by label
goalforge approval reject APR-1 --category code_quality --note "..."
goalforge pr --work-item WORK-1            # a PR body carrying the goal, criteria, and evidence
goalforge checkpoint --next-action "..."   # also writes continuity/<project>.md beside the DB
goalforge pause | resume | cancel
goalforge serve --addr 127.0.0.1:8787      # dashboard + JSON API + Prometheus /metrics
goalforge approval request --action protected-files|publish-branch|merge-branch [--work-item WORK-1] --reason "..."
goalforge approval approve APR-ID
GOALFORGE_POSTGRES_DSN='postgres://...' goalforge storage postgres migrate
```

### MCP server

GoalForge management is exposed over the Model Context Protocol, so MCP
clients (Claude Code, editors, other agents) can inspect and steer projects
conversationally — 15 tools covering status, goal/backlog editing, triage,
approvals, usage/quota reports, run replay, checkpoints, and enqueueing the
autonomous CONTINUE job.

```sh
goalforge mcp                                # stdio transport
claude mcp add goalforge -- goalforge --db /path/to/goalforge.db mcp

goalforge mcp --addr 127.0.0.1:8799          # Streamable HTTP (POST /mcp)
goalforge mcp --addr 0.0.0.0:8799 --token S  # remote: bearer token required
claude mcp add --transport http goalforge http://HOST:8799/mcp --header "Authorization: Bearer S"
```

HTTP mode is stateless JSON-RPC per POST; binding beyond localhost without
`--token` (or `GOALFORGE_MCP_TOKEN`) is refused, and browser `Origin`
headers are validated against localhost to block DNS rebinding. Triage
tools accept only backlog transitions; execution states, merges, and
publishes stay behind the same approval gates as the CLI.

### Environment variables

| Variable | Purpose |
| --- | --- |
| `GOALFORGE_DB` | SQLite path (default `.goalforge/goalforge.db`; also `--db PATH`) |
| `GOALFORGE_CLAUDE_BIN` / `GOALFORGE_CODEX_BIN` / `GOALFORGE_QWEN_BIN` / `GOALFORGE_OPENCODE_BIN` | Provider CLI binary override |
| `GOALFORGE_WEBHOOK_URL` | Slack-compatible JSON webhook for WAITING_QUOTA / BLOCKED / COMPLETED |
| `GOALFORGE_MCP_TOKEN` | Bearer token for the MCP Streamable HTTP transport |
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

Merge and publish approvals are bound to the work item, the verified commit
SHA, and the destination (default branch or remote) resolved when the approval
is requested. An approval therefore covers one reviewed change: it cannot be
spent by another work item, and if the work item is re-run and produces a new
commit the approval is reported as stale so the new change is reviewed instead
of inheriting the old decision.

`goalforge verify template` installs a starting set of gates for a kind of
project without replacing thresholds someone chose deliberately, and
`goalforge project profile` expresses an operating posture — personal, team,
production — as the budget, concurrency, and repair limits that implement it.
`goalforge pr` emits a pull request body carrying the goal, the work item's
purpose and acceptance criteria, the gate results, the completion criteria it
moves, the changed files, and the trailers linking them, so a reviewer sees
what was achieved and how it was proven without reconstructing it from commits.

Improving the automation needs a fixed yardstick, so a prompt, model, or
policy change can be told apart from the work that happened to come up.
`goalforge eval` registers representative cases by kind, attaches completed
runs to them under a configuration label, and compares labels by the verified
pass rate, the cost per run, and the manual interventions each needed —
passing means every required gate passed, not that the run finished. Every run
is stamped with a fingerprint of the configuration it executed under.
Rejections record why the work was turned down, so the recurring reason is
visible rather than buried in individual approvals, and `goalforge report` adds
the rework rate, how often automation stopped for a person, and the median
approval wait.

When automation cannot finish something, two things make handing it to a person
cheap. `goalforge reproduce` writes the commit, the workspace, the exact gate
commands, their output, and the environment diagnosis as a runnable package —
it does not try to make the model produce the same output again, which is not
reproducible and not what investigating a failure needs. `goalforge takeover`
is more than a pause: it refuses while a run is still executing, transfers the
workspace, stops the planner from claiming the item, and on return re-runs the
gates so a hand edit is verified like any other change.

A passing verification result is a statement about a particular tree checked by
a particular command. When the gate changes, or work is merged into the default
branch, the affected evidence is marked as needing re-verification and stops
counting toward completion until it is re-run — `verify integration` records
the integrated result as the new current evidence. Changes that make passing
easier rather than making the result better — a lowered threshold, a required
gate turned optional, deleted test files — are recorded and shown for review
rather than blocked: relaxing a standard can be the right call, but it must not
be mistaken for progress.

Every execution prompt now carries an assembled context package for its work
item: the settled design decisions with the commit they were made against, the
change constraints, what previous attempts at the same item actually failed on,
and the gates and criteria that will judge the result — each line with its
source and date. It is appended with the rules that make it usable: a decision
is settled unless the session proposes changing it as its next action, a
previously failed approach may not be retried without saying what changed, and
verification is never relaxed. Decisions are recorded rather than deleted; one
is superseded by another, because the record of what was rejected is what stops
it being proposed again.

Work items may declare several predecessors, and a dependency that would close
a cycle is refused where it is created. `project concurrency --wip N` raises
how many items may be implemented at once, but items still only run together
when their declared change scopes are disjoint: two sessions editing the same
files in separate worktrees produce a conflict neither of them verified. For
the same reason a merge marks the default branch as needing integration
verification — each item verified inside its own worktree, and nothing has yet
verified their combination — which `goalforge verify integration` clears.

`goalforge models` compares the approved models by the verified success rate
their runs actually achieved, not by whether the provider call returned, and
selects between the configured model and the approved fallback only when the
evidence is strong enough, always with the reason. Token forecasts are a range
with a sample count and a stated confidence rather than a single number, and
the estimate error against actual usage is tracked.

A failed verification is classified before it is retried: test and build
failures and unmet thresholds are repairable by a code fix, while a missing
tool, an unreachable host, an expired credential, or a dependency that will
not resolve are not — re-running a model against those only spends budget.
Automatic repair is capped by attempts and by the cost already spent repairing
the same work item (2 attempts and $5 by default), and the worker stops
rescheduling as soon as the failure is one it cannot fix. `goalforge report`
summarizes a window of automated work: what finished, what stopped and why,
what is waiting on a decision, and what it cost.

The dashboard's live view streams run events over Server-Sent Events with
incremental fetches, and shows its own connection state: a feed that went quiet
because the connection dropped must not look like a run that went quiet because
nothing is happening.

Completion is judged over in-scope work only. `DISCARDED` items leave the
goal's baseline rather than counting as outstanding, so dropping an idea
restates the denominator instead of making completion unreachable; required
completion criteria still have to be met with verification evidence. A gate
with `--value-pattern` records the value it measured from its own output and
fails when that value is below `--success-value`, so numeric criteria such as
coverage are proven by measurement rather than by configuration.

A first project can be set up from the dashboard as well as the CLI: the
`#/new` flow walks repository → environment diagnosis (the same checks as
`goalforge doctor`, which now blocks on a directory that is not a repository)
→ goal → completion criteria → execution policy, and refuses to continue while
a blocking diagnostic stands.

The dashboard at `serve` follows the decision, not the data model. The home
view leads with what needs a decision — approvals, projects needing repair,
budgets near a limit — each with its cause, effect, and recommended action. A
project opens on an overview (current item, next item, last run, expected cost)
with plan, runs, verification, and cost behind tabs. Work items have a detail
page with an editable specification (objective, acceptance criteria, scope,
dependency, estimate) and the blockers that explain why an action is
unavailable; saving a specification never changes status. A run opens as a
change review: verdict, gates with full output, changed files, the diff, and
the prompt that caused it. An approval opens with its grounds: the commit, the
files, the gates, the destination, and how to undo it.

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
