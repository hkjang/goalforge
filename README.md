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
check the installed CLI against every flag the adapter passes. The check reads
the CLI's own help, including the subcommand pages — codex documents `--json`
and `--output-schema` only under `codex exec --help`, opencode `--format` only
under `opencode run --help` — and a flag it cannot find there is reported as
unverified rather than unsupported, because absence from the documentation is
not absence from the CLI: qwen accepts `--approval-mode` and documents it on no
page at all.

## 설치

각 릴리즈에는 바로 실행할 수 있는 바이너리가 플랫폼별로 붙어 있습니다
([releases](https://github.com/hkjang/goalforge/releases)). 압축을 풀고 실행하면
끝입니다 — Go 툴체인도, 런타임도 필요하지 않습니다 (SQLite 드라이버가 순수 Go 라
`CGO_ENABLED=0` 으로 정적 빌드됩니다).

```sh
tar -xzf goalforge_vX.Y.Z_linux_amd64.tar.gz
./goalforge version       # 어떤 빌드인지 먼저 확인
./goalforge doctor
```

같은 릴리즈의 `SHA256SUMS` 로 내려받은 파일이 올라온 것과 같은지 확인할 수 있고,
그 파일이 **이 저장소의 이 커밋에서 빌드되었는지**는 출처 증명으로 확인합니다:

```sh
sha256sum -c SHA256SUMS --ignore-missing
gh attestation verify goalforge_vX.Y.Z_linux_amd64.tar.gz --repo hkjang/goalforge
```

체크섬은 받은 파일이 올라온 파일과 같다는 것까지만 말합니다. 올라온 파일이
어디서 왔는지는 말하지 않습니다 — 그것이 출처 증명이 답하는 질문입니다.

소스에서 빌드하려면 `go build ./cmd/goalforge`, 릴리즈 산출물을 직접 만들려면
`scripts/build-release.sh vX.Y.Z` 입니다. 후자는 linux/darwin/windows 의
amd64·arm64 여섯 조합을 `-trimpath` 로 빌드하고, 각 압축 파일에 바이너리와 가이드를
넣고, 체크섬 파일을 만듭니다. 버전·커밋·빌드 시각은 링크 시점에 박히므로
`goalforge version` 이 스스로를 정확히 밝힙니다.

## 사용 가이드

- [docs/GUIDE.md](docs/GUIDE.md) — 설치부터 운영까지
- [docs/SELECTION.md](docs/SELECTION.md) — 구성 변경이 개선인지 판정하는 규칙 (노이즈 대역·비용 규칙)
- [docs/STANDARDS.md](docs/STANDARDS.md) — 여러 프로젝트에 같은 요구를 적용하는 공통 개발 기준

처음부터 끝까지 한 번 돌려 보는 순서는 [docs/GUIDE.md](docs/GUIDE.md) 에 있습니다.

## Quick start

```sh
goalforge doctor                       # verify git, provider CLI, flags, auth
goalforge project init --name demo --provider claude --model haiku \
  --worktrees --auto-commit --fallback-model sonnet
goalforge goal set --title "Ship the feature" --objective "..." \
  --criterion build_passed=true --criterion note_saves@journey=true
goalforge verify gate add --type build_passed --command-json '["go","build","./..."]' --kind build
goalforge verify gate add --type note_saves --command-json '["./scripts/journey-save.sh"]' --kind journey
goalforge work add --title "Implement session store" --priority 90 --scope "internal/session/**"
goalforge continue                     # one verified work item
goalforge status
```

## Command reference

### Setup and planning

```sh
goalforge doctor [--probe-auth]        # environment and project-readiness diagnostics
goalforge project init --name N [--repo .] [--provider codex|claude|qwen|opencode] [--model M]
                       [--fallback-model M] [--worktrees] [--auto-commit]
goalforge project budget --tokens 2000000 --cost-usd 100 --daily-runs 20 --daily-tokens 250000 --daily-cost-usd 15
goalforge project runtime --turn-timeout 30m --run-timeout 2h
goalforge project concurrency --wip 2      # only items with disjoint change scopes run together
goalforge project profile personal|team|production   # an operating posture as one set of limits
goalforge project sandbox [--mode docker --image golang:1.23] [--memory-mb N] [--cpus N] [--network]
goalforge project provider set --provider claude --model sonnet --reason "..."
goalforge goal contract --title T --outcome "key|method|judge" --measure "p95=latency_ms<=200"
                        [--users ...] [--exclude ...] [--reason ...] [--decider ...]
goalforge goal contract show           # required outcomes, what is unconfirmed, what conflicts
goalforge goal draft --topic "..." [--apply]   # a topic becomes criteria and the gates for them
                   # Criteria and gates are drafted together: asked separately a model produces
                   # a list of aspirations and a list of scripts that do not meet. Each gate is
                   # run against the current tree and must FAIL — one that passes before the
                   # work is done will pass after, so it is measuring nothing. Nothing is
                   # written until a person confirms: a machine that sets its own bar has not
                   # been measured against anything, it has agreed with itself.
goalforge goal set --title T --objective O --criterion build_passed=true [--criterion NAME@journey=true] [--reason ...]
                   # numeric criteria carry a direction and a unit: "<=200ms", ">=99.9%", "=0".
                   # A bare number still means "at least". Units convert within a family
                   # (time, bytes, percent) and a cross-family comparison is refused rather
                   # than guessed, so a latency target is never settled by a memory reading.
goalforge goal show
goalforge milestone add --title T --weight 2
goalforge work add --title T --priority 90 --weight 3 --estimated-tokens 12000 --scope "internal/session/**"
                   [--depends-on WORK-1,WORK-2]   # every predecessor must be DONE first
goalforge work list | work status ID --set APPROVED
                   # IN_PROGRESS, VERIFYING and DONE belong to the engine. A person moving a
                   # card there would be claiming a verification that never ran, so the store
                   # refuses it identically from the CLI, the API and MCP.
goalforge work scope --list-unusable                 # items that can never run
goalforge work scope --item ID --set "internal/server/handler.go"
                   # The scope is compared against file paths, so a scope that is a sentence
                   # matches nothing — and every file the session writes is reported as out of
                   # scope. Such an item fails every run, and there was no way to correct one.
                   # A generator that was not told the format produced whole batches of them.
goalforge unblock --reason "fixed the change scope"
                   # A project stopped by a policy violation never checkpointed, so the resume
                   # had nothing to resume from and the project stayed BLOCKED after the cause
                   # was fixed. The reason is required: clearing a block is a judgement that
                   # what stopped the project was dealt with, and that judgement is the only
                   # thing between a violation and ignoring it. Items left IN_PROGRESS by the
                   # stopped run go back on the board, or they hold the WIP slot forever.
goalforge standards profile --list-packs    # the catalogues this build carries
goalforge standards profile --attributes "deployment=cli,release=binaries"
                   # the project's shape decides which catalogue describes it. Holding a
                   # command-line tool to a web service's criteria reports a dozen gaps it
                   # does not have, and the operator learns to read past the report. With
                   # nothing declared there is no suggestion: a catalogue chosen on no
                   # evidence is one nobody can reconstruct the reason for.
goalforge standards assess [--commit SHA] [--supply] [--supply-limit 3]
                   # reads the repository at a pinned commit. A static read can prove an
                   # absence — a CDN URL in a bundled asset, a fifth required environment
                   # variable — and cannot prove that anything works, so it reports UNMET
                   # where it sees a gap and UNKNOWN everywhere else, never MET.
goalforge standards pass [--interval 24h] [--backlog-floor 3] [--daily-budget 4]
                   # runs a pass only when there is a reason: the branch moved, the board
                   # emptied, or the interval elapsed. Re-reading an unchanged commit gives
                   # the same answers and spends budget doing it. The discovery budget is
                   # separate from the implementation budget — a loop that can borrow from
                   # implementation spends the day deciding what to do and none of it doing it.
goalforge browser check                     # is the playwright-player service reachable?
goalforge browser run --script KEY [--base-url URL] [--variables k=v]
                   # runs a browser journey on the service and exits non-zero when it did not
                   # pass. Three failures look like success and each is refused: an unreachable
                   # service, a run that executed nothing (exit 0 because there was nothing to
                   # fail), and a run still going when the clock ran out.
goalforge capture status                    # which documented screens still depict the code
goalforge capture run [--gate]              # take the screenshots that need taking
                   # A screenshot claims the product looks like this now. Staleness is not
                   # "taken at an older commit" — that would mark every picture stale after
                   # any commit, the report would be permanently red, and people would stop
                   # reading it. What makes a capture stale is a change under that screen's
                   # own declared sources.
goalforge browser gate --type T --script KEY --settles ID
                   # registers the gate and its criterion claim together, and declares the
                   # route and screenshot evidence a journey run produces.
goalforge standards gate --type T --settles ID[,ID] [--produces route,screenshot]
                   # which criteria a gate settles. Declared, not inferred: matching by kind
                   # alone would let any journey test settle every journey criterion.
                   # A gate of the wrong kind is not believed even when it names the criterion.
goalforge standards settle [--commit SHA]   # re-judge the criteria the project's gates claim.
                   # The only path by which a criterion reaches MET — a static read proves
                   # absences and cannot prove that anything works.
goalforge standards autonomy --enable [--standards ID|--all-standards] [--scopes S|--all-scopes]
                             [--merge] [--max-tokens N] [--daily-limit N] [--save]
goalforge standards autonomy --full --save  # every criterion, every scope, merge included, saved
goalforge standards autonomy --show         # what is saved and therefore running unattended
goalforge standards autonomy --history      # what automation approved and how each attempt ended
                   # The envelope has to be saved, not passed as flags: one that only exists
                   # as arguments to a command somebody types applies exactly when somebody is
                   # already there, which is the one time it was not needed. The worker reads
                   # the saved envelope; a project without one gets nothing approved.
                   # The strongest rule survives every flag: work no gate can judge is never
                   # approved. The machine would write code and nothing could say whether it
                   # worked, so the change would land as done on the strength of having been
                   # attempted. With --merge, a release additionally needs its gates to have
                   # passed on the very commit being released, no gate left unrun on that
                   # change, and a default branch that is not known broken.
                   # Each attempt is settled when its verification ends, which is what stops
                   # the loop re-approving a failing item every sweep to reach the same place.
                   # A failure the repair policy will retry is still outstanding — settling it
                   # would refuse the retry the policy just granted.
goalforge config calibrate --label L        # measure how much this project's evaluation moves
goalforge config judge [--incumbent L]      # is a configuration change an improvement?
                   # Sorting configurations by pass rate presents whichever one drew the best
                   # sample as the best configuration. A gain smaller than the measured noise
                   # band is the measurement moving, not the configuration; added cost has to
                   # be paid for by measured gain; and a small sample cannot win by having had
                   # the fewest chances to fail. Nothing is judged before the band is measured.
goalforge config selection [--free-cost F] [--cost-per-gain G] [--min-trials N]
goalforge config direction                  # what the search should try next
goalforge config draft [--repairs 2] [--record] [--apply]  # ask the provider for the next change
                   # The proposer is a model, so what it writes is the product of what it is
                   # shown: the edits already tried with their measured outcomes, the
                   # explanations that did not hold, this round's budget. The hypothesis is
                   # required by the schema rather than asked for in prose, and a refused
                   # draft goes back with the objections — a proposer told only that it failed
                   # writes a variation of the same thing. A draft may carry the setting and
                   # value to change, and --apply puts it into effect linked to its record,
                   # so nobody retypes the field into another command.
goalforge config apply --field wip_limit --to 3 [--proposal PRP-…]
                   # alter a setting, recording what it replaced and which proposal asked
goalforge config settle --incumbent L --candidate L # judge it, and put it back if it did not help
goalforge config changes                            # what automation has altered
                   # Settling writes the verdict back onto the proposal that asked for the
                   # change, which is what fills the falsified set. Without that every record
                   # stays unjudged and the search redraws ideas it has already tested. A
                   # proposal applied and not yet judged counts as neither held nor refuted.
                   # Applying is only meaningful paired with reverting: a change applied and
                   # never judged is worse than no change, and one left in place after failing
                   # is the configuration drifting on its failures. Only enumerated settings
                   # can be changed, because only those have code that knows how to put them
                   # back, and only one change may be outstanding — two before either is
                   # measured make the measurement unattributable.
goalforge config propose --edit "component:hypothesis[:detail]"
                   # screens a candidate before any evaluation is spent. A hypothesis already
                   # falsified is refused — a search that does not remember what it tested
                   # keeps drawing the most plausible idea, which is the one that failed first.
                   # A change naming an evaluation case is refused too: the harness is evolved
                   # against the cases it is measured on, so such a change raises the score
                   # without the product improving, and the score cannot tell the difference.
goalforge standards compare                 # one pack across every project pinned to it
                   # Also raises the cases where the catalogue, not the projects, looks wrong:
                   # a criterion most of a fleet has excused is not one most of a fleet is
                   # failing — it is one that does not fit the work these projects do.
goalforge pattern add|apply|list|approve|retire
                   # A fix that worked in one project is a fix. Calling it a pattern and
                   # recommending it everywhere is how one team's local quirk becomes a
                   # standard nobody chose, so promotion needs two distinct projects — and a
                   # pattern that has failed three times since its last success is retired.
goalforge standards status            # every criterion in force, including the unexamined ones
goalforge standards except --standard ID --reason R --decider D [--review-when W | --review-by DATE]
goalforge verify template go-api|node-frontend|python-library|docs [--overwrite]
goalforge verify gate add --type T --command-json '["go","test","./..."]' [--success-value 100] [--kind build|test|integration|journey|security|performance|review]
                          [--value-pattern 'coverage:\s+([0-9.]+)%']   # measure, do not assume
```

### Discovery, execution, replanning

```sh
goalforge ideas                        # DISCOVER_IDEAS: read-only isolated discovery
goalforge audit                        # AUDIT_AND_IMPROVE: quality/security/perf/UX/ops inspection
goalforge replan                       # REPLAN_GOAL: gaps filed, stale backlog flagged for review
goalforge plan [--json]                # what the next run would do, without doing it
goalforge continue [--enqueue]         # CONTINUE_GOAL: one work item (or schedule for the worker)
goalforge develop                      # IMPLEMENT_SELECTED: highest-priority approved idea
goalforge run --until-quota --max-runs 100
goalforge worker [--standards-every 15m]
                   # also sweeps the standards loop for every enrolled project. The cadence is
                   # only how often the question is asked; whether a project is assessed is its
                   # own interval, discovery budget and backlog floor.
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
goalforge effects [--reconcile]        # what was changed outside, and settle anything unresolved
goalforge tui [--refresh 5s]           # terminal dashboard: progress, criteria, work, approvals, plan
goalforge service systemd [--scope user|system]  # a systemd unit for the worker
goalforge storage usage                # where the state database's space went
goalforge storage prune --older-than 30d [--apply] [--vacuum]
goalforge integrity verify [--json]    # detect edited, deleted, or unchained evidence and approvals
goalforge backup --out FILE            # consistent copy of the state database
goalforge restore --from FILE --to PATH  # verify the records, settle outside work, then resume
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
goalforge decision add --title T --decision "..." [--scope "internal/session/**"] [--alternatives "..."] [--consequences "..."] [--supersedes DEC-1]
goalforge decision list [--all]            # settled architecture, with whether each one still holds
goalforge evidence export --out ./evidence        # handover/audit bundle as self-contained HTML + JSON
goalforge reproduce --run RUN-1 [--out ./repro]   # commit, workspace, gate commands, logs, environment
goalforge takeover --work-item WORK-1 --reason "..."        # stop automation and take the workspace
goalforge takeover return --work-item WORK-1 --summary "..." # re-verify and hand it back
goalforge eval add --name N --kind bug_fix|feature|refactor|docs --objective "..."
goalforge eval record --case EVAL-1 --label "haiku+v2" --run RUN-1
goalforge eval spec --case EVAL-1 --fixture ./fixture --ref COMMIT --criterion build_passed=true
                    --work "작업 제목" --gate-type build_passed --gate-command-json '["go","test","./..."]'
                    [--token-budget N] [--cost-budget-usd N] [--timeout-seconds N]
goalforge eval from-failure --run RUN-1                          # turn a real failure into a case
goalforge eval from-failure --approval APR-1                     # turn a rejection into a case
goalforge eval run --case EVAL-1 --label "haiku+v2" --repeat 3   # clean environment per repetition
goalforge eval run --case EVAL-1 --label "haiku+v2" --repeat 3 --arm baseline  # same model, no GoalForge
goalforge eval compare [--case EVAL-1]     # pass rate, stability, cost per success, and the baseline delta
goalforge approval reject APR-1 --category code_quality --note "..."
goalforge pr --work-item WORK-1            # a PR body carrying the goal, criteria, and evidence
goalforge checkpoint --next-action "..."   # also writes continuity/<project>.md beside the DB
goalforge pause | resume | cancel
goalforge serve --addr 127.0.0.1:8787      # dashboard + JSON API + Prometheus /metrics
                   # the Board tab is a kanban over the same work items: priority, dependency
                   # counts, blocker reasons and the verification outcome on each card, with a
                   # separate delivery badge. DONE means this item's own gates passed; whether
                   # it merged, whether the merged result holds up, and whether it may ship are
                   # three further facts a column cannot carry without claiming one for another.
goalforge approval request --action protected-files|remove-tests|publish-branch|merge-branch [--work-item WORK-1] --reason "..."
goalforge approval approve APR-ID
GOALFORGE_POSTGRES_DSN='postgres://...' goalforge storage postgres migrate
```

### Sharing the job queue across machines

By default the job queue lives in the same SQLite database as everything else,
so `goalforge worker` only sees work enqueued on its own machine. Setting
`GOALFORGE_POSTGRES_DSN` moves the queue and the project leases to a shared
PostgreSQL server, which is what lets a worker on one machine pick up work a
dashboard on another requested.

```sh
export GOALFORGE_POSTGRES_DSN='postgres://user:pass@host/goalforge?sslmode=require'
goalforge storage postgres migrate     # once, to create the tables
goalforge serve                        # enqueues into PostgreSQL
goalforge worker                       # drains PostgreSQL
```

Every process in the deployment must see the same value. `worker`, `serve`,
`mcp`, and `continue --enqueue` each resolve the queue once at startup and say
which one they are using, because a deployment where the dashboard enqueues
into one queue and the worker drains another has a button that reports success
and never runs. For the same reason a DSN that is set but unreachable is a
startup error rather than a quiet fall back to the local queue.

PostgreSQL coordinates *who runs what*. Goals, work items, runs, and
verification evidence remain in each machine's SQLite database — it is not yet
the store of record for project state, so the shared queue is for a pool of
workers against shared checkouts, not for splitting one project's history
across machines.

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

Verification gates run code the session just wrote, so they are not more
trusted than the session. `project sandbox --mode docker --image IMG` runs them
in a container with the workspace mounted and nothing else: no host filesystem,
no network unless the project asks for it, no capabilities, a read-only root,
and ceilings on memory, CPU, and processes. It runs as the invoking user rather
than root, so the workspace stays writable without handing back the capability
that lets root ignore file permissions. The default is still the host, because
a sandbox that cannot run the project's toolchain is worse than none and only
the project knows which image can.

A goal that still has work records that intent in the same transaction as the
outcome that decided it, and a worker turns those records into scheduled work
when it starts and on every tick. A crash between committing the outcome and
scheduling the follow-up therefore leaves the goal continuing on restart rather
than silently stopped, and publishing twice produces one job.

A run holds a lease with a generation. Taking over an expired lease, or
cancelling, starts a new generation, and a worker carrying the old one can no
longer confirm state: its work may have been correct, but something else has
been running the project since and a late confirmation would overwrite that. A
cancel that lands while a provider is mid-turn therefore stops the run from
marking work done or recording a commit, rather than racing it. Pausing does
not end the tenancy, because the work is meant to continue. A caller with no
lease is the operator running a command directly and is not fenced.

Pushing and merging change something outside this database, so a crash between
doing one and recording it leaves the two disagreeing. Both are written to an
effect ledger before they are attempted and settled afterwards. A failure whose
outcome could not be determined is stored as unknown rather than failed —
retrying a failure is safe, retrying something that may have succeeded is not —
and the next attempt asks the remote whether the change is already there before
doing anything. If the question cannot be answered, nothing is retried until it
is; `goalforge effects --reconcile` settles what it can and reports what it
cannot. There is no globally exactly-once execution being assumed here, only a
record that makes a duplicate detectable.

Merge and publish approvals are bound to the work item, the verified commit
SHA, and the destination (default branch or remote) resolved when the approval
is requested. An approval therefore covers one reviewed change: it cannot be
spent by another work item, and if the work item is re-run and produces a new
commit the approval is reported as stale so the new change is reviewed instead
of inheriting the old decision.

A gate declares what it establishes and a criterion declares what would settle
it. `--kind` on a gate and `name@kind=value` on a criterion are what separate
"it compiles" from "the user can do the thing": a screen can be finished, the
build green, and the save button wired to a stub, and a build gate passing is
true while saying nothing about whether a note can be saved. When the gate
measuring a criterion proves a weaker kind than the criterion demands, the
criterion reports `WRONG_KIND` rather than `MET` — the goal cannot complete, and
the message names the kind asked for and the kind that answered, because the fix
is to replace the check, not the code. Stronger evidence settles a weaker
demand: a journey that ran necessarily compiled. Security and performance are
specific properties nothing else implies. A criterion that demands no kind
behaves exactly as before, so adding this does not unmet existing projects, and
relabelling a gate marks its old evidence for re-measurement rather than
retroactively promoting it.

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
`goalforge eval from-failure` closes the improvement loop. Until a real
failure becomes a repeatable case, "we fixed the prompt" is a claim about a run
that can never be run again: the repository has moved on and the only record is
a log. The case pins the commit the failed run *started* from — not the current
state, where the fix may already be present — together with the goal's
criteria, the gates that were in force, and the work item that failed, and it
records what kind of failure it was so a suite says where a configuration is
weak rather than only that it is. A rejected approval makes a case too, and a
more valuable one: the gates passed and a person still said no.

"GoalForge completes N% of tasks" is not a claim about GoalForge unless
something else was measured the same way. `--arm baseline` runs the same case
with the same model, tools, workspace, and budget, given the task directly —
no decomposition, no verification-repair loop, no evidence ledger — and judged
by the same gates with the same criteria and the same rule about which kind of
evidence settles which criterion. The baseline prompt carries the gate commands
too: withholding them would measure GoalForge's prompt rather than its
orchestration. Where the two arms cannot be set against each other, the
comparison says why instead of printing a difference, and the condition hash
covers what must be held equal while excluding the label and the arm, which are
the variables under test.

`goalforge eval spec` pins a case to a fixture repository at one commit, with
the criteria and gates that judge it and the limits it runs under, and
`goalforge eval run` re-executes it — each repetition in a freshly cloned
workspace with a state database that has never seen another trial. Reusing
either is how a previous attempt's answer, or its recorded state, leaks into
the next measurement. A workspace that does not start from the pinned state is
recorded as an invalid trial rather than scored or silently dropped.

Trials and attached runs are reported separately and never averaged together:
a trial measures the configuration, an attached run measures the run that was
attached. Repetition stability — the share of cases where every repetition
passed — is reported next to the single-run rate, because a tool that succeeds
once and one that succeeds every time are not equally trustworthy. Trials that
could not be measured are excluded from the rate and counted on their own, and
cost per success includes the failed attempts.

A case that seeds its starting backlog measures implementation; one that leaves
it empty measures the configuration's own goal decomposition too. Which of the
two a suite is doing is part of the pinned case rather than a runtime default.
`goalforge eval` also still attaches completed runs to cases under a label — Every run
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
a particular command, so each result records both: the workspace and tree it
measured, and a fingerprint of the gate that measured it. Staleness is then
detected wherever current state is read — `status`, `plan`, the dashboard —
rather than depending on every write that changes the inputs remembering to
declare it. Evidence from a work item's isolated worktree is never compared
against the default branch, because it was never a statement about it; that is
what integration verification is for. When the gate changes, or work is merged
into the default branch, the affected evidence is marked as needing
re-verification and stops
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

A goal contract states what the goal commits to in a form that can be judged:
who it is for, what it must achieve, how each required outcome will be settled
and by whom, what is deliberately out of scope, and the budget and deadline it
runs under. A requirement with no method or no judge is kept and marked
unconfirmed rather than accepted or dropped — "업계 최고 수준" is not an outcome
until someone says what would settle it, and until then the goal cannot be
judged met. Requirements that cannot both hold, such as one metric needing to
be both above and below a threshold, are reported as conflicts with both sides
intact: dropping one to resolve it is how a contract quietly becomes a
different contract.

Changing a contract creates a new version with a reason and a decider, and
every earlier version keeps the outcomes it required. Narrowing the goal
therefore cannot turn a missed goal into a met one after the fact.

`goalforge plan` shows what the next run would do without doing any of it: the
work item that would be chosen and why higher-priority ones were skipped, the
model and the reason it was selected, the expected tokens and cost against the
remaining budget, the gates that would judge the result, and every precondition
that would refuse the run. It shares its selection rules with the real claim,
so the preview cannot promise work the run would reject — and it distinguishes
what would block a run from what would merely stop the goal from ever being
judged complete. Spending a model call to discover an exhausted budget or a
missing gate is the expensive way to learn it.

`goalforge evidence export` writes the case for what a goal achieved and how it
was proven: goal versions with their change reasons, each work item's commit
and gate results with measured values, design decisions, the approval history
including what was refused and why, and the checks that were relaxed. It is a
self-contained HTML page plus the same record as JSON, and the dashboard serves
it at `/api/v1/projects/{id}/evidence`. The refusals and relaxations are in
there deliberately: a bundle that only keeps the good news describes a
different project than the one that happened.

A contract states what a goal requires, and it now decides whether the goal is
finished rather than only warning about it. A required outcome with no method
or no judge cannot be settled, and two that cannot both hold mean the goal has
no achievable definition — in either case the work being done says nothing, so
completion is refused on every path rather than in the plan preview alone. The
reason a goal is not finished is computed once and shown the same way by the
CLI, the dashboard, and the terminal UI.

A worker left running accumulates: every event a provider emits is stored
whole, and nothing removed them. `goalforge storage usage` says where the space
went and `goalforge storage prune` drops the bulk of finished runs — the bodies
go and the rows stay, so an event keeps its type, its time, and its hash and a
prompt keeps its template and its hash. The audit still answers "what happened
and when" and stops answering "in exactly these words", which is the part that
costs gigabytes and the part least often needed. Evidence, approvals, and the
integrity chain are never touched: removing one would make `integrity verify`
report tampering, correctly and unhelpfully. It reports before it removes,
because audit data deleted on a typo does not come back.

Everything GoalForge claims rests on two kinds of record: the evidence that
says a goal is done, and the approvals that say a change was allowed out. The
session GoalForge orchestrates has write access to the same database file, so
a row nobody can distinguish from one GoalForge wrote is not evidence. Both
kinds are linked into an append-only chain as they are written, and
`goalforge integrity verify` reports three distinct findings: a record edited
after it was written, a record deleted, and a record inserted without passing
through GoalForge at all.

Set `GOALFORGE_AUDIT_KEY` to make the chain unforgeable. Without it the chain
still catches an edit, a deletion, and a direct insert, but whoever made them
could recompute the chain; with it they cannot, because the links are MACs.
`integrity verify` says which of the two is in force rather than implying the
stronger one. The key — along with `GOALFORGE_API_TOKEN`, `GOALFORGE_MCP_TOKEN`,
and `GOALFORGE_POSTGRES_DSN` — is withheld from every execution session, and
that list is not widened by `GOALFORGE_PASS_ENV` or `GOALFORGE_INHERIT_ENV=all`:
an operator widening their environment is saying "this session may see my
tooling", not "this session may hold the keys to its own audit".

`goalforge service systemd` emits a unit that runs the worker as a Linux
service. Written by hand, three things go wrong: the binary path, the state
database path, and the working directory — a worker started from the wrong
directory finds no registered project and drains an empty queue while looking
healthy. The unit is generated from the running process, so all three are the
ones that are actually correct. It prints rather than installs, because writing
into `/etc` is the operator's decision and not a side effect of asking what the
unit should say. Secrets are pointed at a credentials file rather than written
into the unit: a unit is world-readable, so a token in one is a token published
to every account on the machine.
A recorded worktree is the same kind of claim about the filesystem. `git
worktree prune`, a manual delete, a fresh clone, a reset disk — and, once a
PostgreSQL queue is shared, a worker on a different machine picking up work
whose worktree was created somewhere else. The recorded path is checked before
anything reads the tree, so the failure says which worktree is gone, that the
committed work is still on its branch, that uncommitted work in it is not
recoverable, and how to recreate it — rather than surfacing as
`fatal: cannot change to '...'`, which reads like the repository is broken. A
path that exists holding a different branch is refused for the same reason:
continuing there would write into somebody else's work.

A project is found by its repository path, so a repository that moves becomes
unreachable: every command reports "no project here" while the goal, the
evidence, and the approvals are all still in the database. The message names
the paths that *are* registered and marks the ones that no longer exist, and
`goalforge project relocate` rewrites the path. It is a rename rather than a
re-registration, because registering again would start a second project beside
the first and leave the history behind.

A stored session ID is a claim about the provider's storage, not GoalForge's.
The provider can discard it at any time — expiry, a cleared cache, a different
machine — and says so only when asked to resume. When a resume fails because
the session is gone, the binding is dropped and the turn is done again from a
fresh one; the conversation is lost either way, and what changes is whether the
goal stops and waits for a person. Only the provider's own phrasings for a
missing session count: a resume that failed on quota or auth still fails, since
starting fresh there would discard a conversation over a transient outage and
hide the real fault behind a retry that looks fine. `goalforge sessions --drop
active` forgets a binding by hand when you already know it is gone.

`goalforge tui` is the same view as the dashboard without a browser: work
progress and proven completion side by side, every criterion with the evidence
behind it, work items with the reason each one is stuck, the approval inbox
with the commit and files each decision covers, and what a run would do right
now. It refuses to start outside a terminal instead of hanging, honours
`NO_COLOR`, and lays out by display width so Korean columns line up. Lists
longer than the screen scroll with the cursor and say how many rows are out of
sight; a list that renders its first screenful and drops the rest moves the
cursor somewhere nobody can see, which is indistinguishable from the key not
working. `?` shows every key on its own screen, because the footer is one line
and truncates exactly where someone is most likely looking for it. Deciding
an approval from it is checked where the decision happens, not when the screen
opens: a session that is refused `goalforge approval approve` is refused the
keystroke too, because opening a screen is not a permission. If any evidence or
approval fails the integrity check, the warning sits above every tab rather
than behind one.

A boundary the CLI enforces and the API does not is not a boundary, and the
session reaches both. `goalforge serve` listens on loopback with no token by
default, and in that mode the API authenticates nobody — the
`X-Requested-With` header defends the browser, it is not a credential. So an
unauthenticated API refuses the operations the CLI already refuses to a
session: deciding an approval, redefining the goal, and changing the budget.
Reads stay open, because watching a run is what the loopback dashboard is for
and closing it would prevent nothing. With `GOALFORGE_API_TOKEN` set, the
caller has presented an operator secret that sessions are never given, so the
decision is attributable and allowed.

`goalforge doctor` checks two different things. The environment checks ask
whether this machine can run anything; the readiness checks ask whether this
project could ever finish, which is where the silent misconfiguration lives: a
completion criterion with no gate of the same name accumulates no evidence, so
the goal runs forever while everything looks healthy. It also catches the
configuration that looks healthiest of all — a criterion about whether the
user's task works, measured by a gate that only compiles — and warns when no
gate in the project claims to do more than build, because then a goal can
complete having never exercised anything. Readiness also catches a goal with no
criteria, gates that are all optional, and gate commands that are not on PATH,
and warns about an unset budget, stale evidence, and pending integration
verification.

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

`status` remains available after goal completion. It reports weighted progress
with the baseline it was computed over, each completion criterion with the
evidence that decided it (met, short of the threshold, needing re-verification,
or never measured), run and work outcomes, verification pass rate, session
count, token categories, and cost. It ends with what needs a person —
approvals, a stopped repair, pending integration verification, work a person
has taken over — so the answer to "what now" does not have to be inferred from
a state code.

Webhook notifications suppress an identical project/state/reason for 30 minutes
(`GOALFORGE_WEBHOOK_REPEAT_WINDOW`, 0 to disable) and carry the project name: a
blocked project re-reports on every worker tick, and a channel that repeats
itself is one people learn to ignore.

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
