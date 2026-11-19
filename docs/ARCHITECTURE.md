# rhymehub Architecture

rhymehub is a self-hosted platform that manages AI-driven development (Claude Code / Codex CLI) for many
projects from one mobile-friendly dashboard, accepts development requests from inside managed apps via an
SDK, and safely updates itself through an independent Supervisor.

Target for v0.1: **one MacBook, no cloud infra, no Postgres/Redis/Kubernetes**.

## 1. Components and processes

```text
                     ┌──────────────────────── launchd (macOS, optional) ───────────────────────┐
                     │  rhymehub-supervisor  (services/supervisor)  127.0.0.1:7781 (token)      │
                     │   - owns versions/, current symlink, updates.jsonl, update journal       │
                     │   - spawns + health-checks core, drains, swaps, rolls back               │
                     └──────────────┬───────────────────────────────────────────────────────────┘
                                    │ spawn / SIGTERM / GET /healthz / POST /api/v1/system/drain
 browser / PWA ── HTTPS/HTTP ──▶ rhymehub serve  (apps/server)  :7780
 managed app SDK ─────────────▶   - REST API + SSE + embedded web (apps/web build)
                                  - auth/RBAC, projects, inbox, scheduler, release manager, notifier
                                  - SQLite (WAL)  $RHYMEHUB_HOME/rhymehub.db
                                    │ spawn detached (setsid)          ▲ events/state via SQLite
                                    ▼                                  │
                                rhymehub worker --task <id>  (services/worker) ── one per running task
                                  - Workspace: git worktree per task (internal/workspace)
                                  - Agent adapter: claude / codex / fake (packages/agent-adapters)
                                  - validation: project test command in worktree
```

* **Core (`rhymehub serve`)** – HTTP API, auth, scheduler, Inbox, release manager, notification service, SSE.
  Never runs agent CLIs in-process.
* **Worker (`rhymehub worker --task ID`)** – a detached child process (new session, survives core restart)
  that prepares the worktree, runs the agent adapter for one turn, runs validation, commits the result in
  the task branch, writes every event and state change to SQLite, then exits. Heartbeat every 5 s.
* **Supervisor (`rhymehub-supervisor`)** – separate binary, separate lifecycle. Starts core from
  `$RHYMEHUB_HOME/current/rhymehub`, restarts on crash, performs verified updates with DB backup, health check
  and rollback. Core never replaces its own binary.
* **Web (`apps/web`)** – React + TypeScript + Vite + Tailwind PWA (manifest + service worker for Web Push).
  Built into `apps/server/web/dist` and embedded with `go:embed`.
* **SDK** – `@rhymehub/sdk` (headless TS client) and `@rhymehub/react` (`<RhymehubFeedback/>` UI).
* **Skills/templates** – markdown skills the agent follows (analysis, bugfix, feature, tests, review,
  release-prep) and `rhymehub init` templates. Skills are procedures, not an execution engine.

Durability rule: **all task state lives in SQLite**. Core keeps only caches. A core restart re-reads tasks,
re-attaches to live workers (pid + heartbeat), and recovers orphaned ones (§5).

## 2. Repository layout (single Go module + pnpm workspace)

```text
go.mod                         module github.com/lafin716/rhymehub
apps/server/                   cmd + HTTP API (package main in apps/server/cmd/rhymehub)
  cmd/rhymehub/                main: serve | worker | init | register | doctor | status | version | migrate
  internal/api/                handlers, router, SSE, middleware (auth, TLS policy, CORS for /sdk)
  internal/scheduler/          queue, limits, spawning workers, recovery, stuck detection
  internal/release/            release manager (merge, version calc, notes, tag, self-build, sign, feed)
  internal/notify/             notification service (dedupe, retry), web push channel, webhook channel
  internal/cli/                init/register/doctor/status implementations
  web/                         embed.go + dist/ (copied from apps/web build; placeholder committed)
services/worker/               worker runner (library) used by `rhymehub worker`
services/supervisor/           rhymehub-supervisor (package main + internal logic)
packages/agent-adapters/       Go: adapter interface, claude, codex, fake adapters
internal/domain/               shared types, IDs, task state machine, roles
internal/store/                SQLite schema/migrations + repository methods (used by core and worker)
internal/workspace/            git CLI wrapper: worktree create/remove, diff, commit, merge-check, merge
internal/redact/               PII/secret filtering for diagnostics and prompts
internal/config/               $RHYMEHUB_HOME layout + config.json
internal/signing/              ed25519 sign/verify + sha256 helpers (shared by release + supervisor)
packages/sdk-typescript/       @rhymehub/sdk
packages/sdk-react/            @rhymehub/react
packages/shared/               shared TS API types (consumed by web + sdk)
apps/web/                      dashboard PWA
examples/feedback-demo/        example managed app (Vite React) using the SDK + test suite
skills/                        agent skills (markdown)
templates/                     files written by `rhymehub init`
scripts/                       build-release.sh, dev.sh, e2e scenario scripts
docs/                          design docs + ADRs + validation reports
```

## 3. Runtime home directory (`$RHYMEHUB_HOME`, default `~/.rhymehub`)

```text
config.json           listen addr, allowed insecure CIDRs, limits, agent defaults, feed url
rhymehub.db(-wal)     SQLite
secrets/              0700: supervisor.token, release-signing.key (ed25519 private), vapid.json
keys/                 release-signing.pub (trusted public key used by supervisor)
versions/<ver>/       installed binaries (rhymehub)
current -> versions/<ver>
releases/             local release feed: manifest.json, rhymehub_<ver>_<os>_<arch>.tar.gz(.sig)
backups/              pre-update DB backups  rhymehub-<ts>-<fromver>.db
worktrees/<project>/<task>   per-task git worktrees (agents are confined here)
attachments/<request>/       uploaded diagnostics (redacted)
logs/                 core.log, supervisor.log, tasks/<task>.log
updates.jsonl         supervisor update history
update-journal.json   in-flight update journal (crash-safe resume/rollback)
```

## 4. Task state machine (internal/domain)

States: `CREATED QUEUED PREPARING RUNNING WAITING_INPUT WAITING_APPROVAL VALIDATING REVIEW_READY APPROVED
MERGING RELEASING COMPLETED FAILED CANCELLED RETRYING ROLLED_BACK`.

| From | Allowed to |
|---|---|
| CREATED | QUEUED, CANCELLED |
| QUEUED | PREPARING, CANCELLED |
| PREPARING | RUNNING, FAILED, CANCELLED, RETRYING |
| RUNNING | WAITING_INPUT, WAITING_APPROVAL, VALIDATING, FAILED, CANCELLED, RETRYING |
| WAITING_INPUT | QUEUED (answer), CANCELLED |
| WAITING_APPROVAL | QUEUED (permission granted/denied, resumes), CANCELLED |
| VALIDATING | REVIEW_READY, FAILED, CANCELLED, RETRYING |
| REVIEW_READY | APPROVED, QUEUED (follow-up message), CANCELLED |
| APPROVED | MERGING |
| MERGING | RELEASING, COMPLETED, FAILED |
| RELEASING | COMPLETED, FAILED |
| FAILED | QUEUED (retry / follow-up), RETRYING, CANCELLED |
| RETRYING | QUEUED, FAILED |
| COMPLETED | ROLLED_BACK |
| CANCELLED, ROLLED_BACK | (terminal) |

Transitions go through `store.TransitionTask(ctx, id, expectFrom []State, to State, ev Event)` which is a single
SQL transaction doing compare-and-set on `state` + `version`, appending a `state_changed` event. Duplicate
deliveries are absorbed by an optional idempotency key (`UNIQUE(task_id, idem_key)`): replaying the same key is a
no-op returning the current task. Illegal transitions return `domain.ErrIllegalTransition`.

## 5. Scheduling and recovery (apps/server/internal/scheduler)

* Tick every 1 s (and on wake-up signal). Picks `QUEUED` tasks ordered by `priority DESC, created_at ASC`
  subject to: global `max_concurrent` (config, default 2), per-project `max_concurrent` (default 1), per-agent
  limit (config, default 2). "Running" = PREPARING|RUNNING|VALIDATING.
* Starting a task = transition QUEUED→PREPARING then spawn `rhymehub worker --task ID` (Setsid, stdout/err to
  `logs/tasks/ID.log`, scrubbed environment – see SECURITY.md). Worker records `worker_pid`.
* Heartbeat: worker updates `heartbeat_at` every 5 s. Scheduler marks tasks with heartbeat older than 60 s and
  dead pid as orphaned → `RETRYING` (attempt < max_attempts, default 2) → `QUEUED`, else `FAILED`.
* Long-running detection: RUNNING longer than project `timeout_minutes` (default 30) → worker kills agent →
  FAILED with reason `timeout`, notification.
* Startup recovery: same orphan check on every task in PREPARING/RUNNING/VALIDATING; live workers keep going.
* Drain (`POST /api/v1/system/drain`): scheduler stops starting tasks; responds with number of active workers.
  Workers are detached and keep running across core restarts, so drain does not need to kill them.
* Cancel: core sends SIGTERM to the worker process group; worker cancels the adapter (kills the CLI process
  group) and transitions to CANCELLED. If the worker is dead, core transitions directly.

## 6. Worker turn (services/worker)

1. Load task + project. If no worktree: `workspace.CreateWorktree(repo, base=default_branch, branch=rhymehub/<task>)`
   under `worktrees/<project>/<task>`; record `branch`, `worktree_path`, `base_commit`.
2. PREPARING→RUNNING. Build the prompt: system preamble (skills + rules + question/permission protocol) +
   the task prompt or the pending message (`next_input`). Request text/diagnostics are inserted as *quoted
   untrusted data*, already redacted.
3. `adapter.Execute(session, prompt)` streams `AgentEvent`s → `task_events` (`agent_text`, `tool_use`,
   `tool_result`, `agent_error`, `usage`). Session id saved on the task for resume.
4. End of turn:
   * final text contains a line `RHYMEHUB_QUESTION: <q>` → WAITING_INPUT (question stored, notification).
   * adapter reports permission denials and capability `permissionRequests` → WAITING_APPROVAL with the denied
     tool list; granting re-queues with those tools added to `allowed_tools`.
   * error / non-zero exit → FAILED (or RETRYING if transient and attempts remain).
   * otherwise → VALIDATING.
5. VALIDATING: `git add -A && git commit` in the worktree (author `rhymehub-agent`), compute diff stat, run
   project `test_cmd` (configured by Developer+, never from request text) with timeout; store exit code + tail.
   Pass (or no test command) → REVIEW_READY; fail → FAILED (`tests_failed`). Notifications sent by core when it
   observes the state event.

## 7. Release & merge (apps/server/internal/release)

* `POST /tasks/{id}/approve` (Maintainer+, blocked if tests failed) → APPROVED → MERGING:
  `git merge-tree --write-tree` conflict check, then merge `--no-ff` into the default branch in the project
  repository (requires the default branch checked out and clean; otherwise FAILED with reason). Conflicts → FAILED
  `merge_conflict` with file list; worktree preserved.
* After merge, release strategy:
  * `none` → COMPLETED.
  * `tag` → compute next semver from latest `v*` tag and change type (feature→minor, else patch), notes from
    commit log, annotated tag; record `releases` row → COMPLETED.
  * `command` → run project `release_cmd` (Maintainer-configured) with VERSION env.
  * `rhymehub-self` → run `scripts/build-release.sh VERSION OUTDIR` from the merged repo, sign artifacts with
    `secrets/release-signing.key`, publish to local feed `releases/manifest.json`, record release. Updating the
    running instance is a separate admin action (or `auto_update` policy) handled by the Supervisor.
* Auto-approve and auto-release are **separate per-project policies, both off by default**.

## 8. Self-update (services/supervisor) — see RELEASE_AND_UPDATE.md

download → verify sha256 + ed25519 signature → journal `phase=verified` → drain core → stop core → checkpoint &
backup DB → extract to `versions/<new>` → swap `current` → start core → health check (`/healthz` reports the new
version, schema ok, within 45 s) → commit (`phase=done`) or rollback (stop new, restore symlink, restore DB backup
if schema version changed, start old, verify health) → append to `updates.jsonl`. On supervisor start, an
unfinished journal is resolved (rollback unless the new version is already healthy).

## 9. Real-time events

* `task_events` table is the event log (monotonic `id`, per-task `seq`).
* `GET /api/v1/events/stream` (SSE): core polls `task_events` + request/notification changes every 300 ms
  (multi-process safe because workers write the DB) and fans out to subscribers; supports `Last-Event-ID`.
* Per-task backlog: `GET /api/v1/tasks/{id}/events?after=<seq>`.

## 10. Agent adapters (packages/agent-adapters) — see AGENT_RUNTIME.md

Go interface mirroring the product spec: `Capabilities()`, `CreateSession`, `Execute` (event channel),
`Respond`, `Cancel`, `Status`, `Recover`. Implementations: `claude` (stream-json, `--session-id/--resume`),
`codex` (`exec --json`, `exec resume <thread>`), `fake` (deterministic, for tests/CI without credentials).

## 11. Tech stack decisions (see docs/adr)

| Area | Choice | Why |
|---|---|---|
| Core/worker/supervisor | Go 1.2x, net/http, single static binaries | macOS-friendly, easy distribution |
| DB | SQLite via `modernc.org/sqlite` (pure Go, WAL) | no cgo, cross-compile for releases |
| Web | React 19 + Vite + TS + Tailwind v4, hand-written shadcn-style components, PWA | spec, light |
| Realtime | SSE | one-way stream is enough; proxies well |
| Git | git CLI + worktrees | exact git semantics, no libgit2 |
| Push | Web Push (VAPID) via `webpush-go`; webhook channel | spec; plugin interface for more |
| Signing | ed25519 + sha256 | stdlib only |
| Process mgmt | launchd LaunchAgent for supervisor | macOS native |
| JS packaging | pnpm workspace | monorepo |
