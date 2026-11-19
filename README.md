# rhymehub

rhymehub is an open-source, self-hosted AI development and app lifecycle hub. It runs on one Mac (Linux works
for the core), and you use it from a mobile or desktop browser (PWA). It drives **Claude Code** and **Codex CLI**
per project in isolated git worktrees. Your apps can file development requests through an SDK. Changes go through
tests, review and approval before release. rhymehub can also update itself through a signed, health-checked and
rollback-capable supervisor.

> Status: **v0.1.0 MVP**. See [Feature status](#feature-status) for what is verified and what is not implemented
> yet, and [docs/VALIDATION_REPORT.md](docs/VALIDATION_REPORT.md) for the evidence.

## Components

| Path | What |
|---|---|
| `apps/server` | `rhymehub` core: HTTP API, auth, scheduler, notifications, release manager, embedded dashboard |
| `apps/web` | Dashboard PWA (React + Vite), embedded into the server binary |
| `services/worker` | Per-task worker that runs an agent turn in a worktree, then runs tests/build |
| `services/supervisor` | `rhymehub-supervisor`: runs the core, verified self-update, drain, health check, rollback, launchd |
| `packages/agent-adapters` | Claude Code / Codex CLI / fake adapters (detection, streaming, resume, cancel) |
| `packages/shared`, `packages/sdk-typescript`, `packages/sdk-react` | App SDK and the React Feedback widget |
| `examples/feedback-demo` | Example app that files requests through the SDK |
| `internal/*` | domain, SQLite store + migrations, config, workspace (git/worktrees), signing, redaction |
| `docs/` | Architecture, API, data model, runtime, SDK, security and release contracts |

## Requirements

- macOS (darwin/arm64 or amd64) or Linux; `git`.
- Optional agents: [Claude Code](https://docs.claude.com/en/docs/claude-code) (`claude`) and/or Codex CLI
  (`codex`), each signed in through its **own official CLI login**. rhymehub never reads their credential stores.
- Building from source: Go 1.26+, Node 20+, pnpm 9+.

## Quick start (release binary)

```bash
# 1. Download and unpack (pick your platform), then check the checksum
curl -LO https://github.com/lafin716/rhymehub/releases/download/v0.1.0/rhymehub-0.1.0-darwin-arm64.tar.gz
curl -LO https://github.com/lafin716/rhymehub/releases/download/v0.1.0/SHA256SUMS
shasum -a 256 -c SHA256SUMS --ignore-missing
tar xzf rhymehub-0.1.0-darwin-arm64.tar.gz      # -> bin/rhymehub, bin/rhymehub-supervisor, VERSION

# 2. Initialize the home directory (~/.rhymehub, or $RHYMEHUB_HOME)
./bin/rhymehub init
./bin/rhymehub doctor          # checks home, DB, git, claude/codex availability

# 3. Run the core directly ...
./bin/rhymehub serve           # listens on 0.0.0.0:7780 by default
# ... or under the supervisor (self-update, health check, rollback)
./bin/rhymehub-supervisor bootstrap
./bin/rhymehub-supervisor run
./bin/rhymehub-supervisor install-launchd --load   # optional: start at login (macOS)
```

Open the dashboard and create the first admin account (the first-run setup page calls `POST /api/v1/setup`):

- `http://localhost:7780`
- `http://<LAN-IP>:7780`
- `http://<host>.<tailnet>.ts.net:7780` (Tailscale MagicDNS) or `http://100.x.y.z:7780`

The server binds `0.0.0.0` so phones on your LAN or tailnet can reach it. Auth is always on. Put it behind TLS
(`tlsCert`/`tlsKey` in `config.json`) or Tailscale before exposing it beyond a trusted network.

## CLI

```
rhymehub <command>
  serve     [--listen addr]          run the API + dashboard + scheduler + notifications
  worker    --task <id>              run one task turn (spawned by the scheduler)
  init                               create home layout, default config.json, migrate DB
  register  [--path dir] [--name n] [--server url]   register a repo (remote uses RHYMEHUB_TOKEN)
  doctor                             environment and agent checks
  status    [--server url]           projects, tasks and workers
  migrate                            apply DB migrations (exit 3 on migration failure)
  version

rhymehub-supervisor <command> [--home dir]
  run [--addr 127.0.0.1:7781] [--feed url]   run and supervise the core
  status | bootstrap | keygen | install-launchd [--load] | sign-release ...
```

## Typical flow

1. **Register a project**: in the dashboard (local path, git URL clone, or discovery), or `rhymehub register` in
   the repo. Set its test/build commands. Only these configured commands are ever executed.
2. **Create a task**: from the project chat, choose `claude`, `codex` or `fake`. The scheduler creates a worktree
   and branch, runs the agent, then runs your tests/build. You follow the streamed events live.
3. **Review**: look at the diff and test results, answer agent questions, grant or deny permission requests,
   send follow-ups, cancel or retry.
4. **Approve and release**: a Maintainer approves, rhymehub merges and runs the release strategy
   (`none`, `tag`, `command`, `self`). Auto-approve and auto-release are separate settings, both **off** by default.
5. **In-app requests**: add the SDK to your app (`@rhymehub/sdk-react` `<Feedback/>`, or `@rhymehub/sdk`) with a
   project key (`rhk_…`). Requests land in the Inbox and can create tasks automatically. The project's
   **SDK setup** tab renders a prompt (or Claude Code skill) you paste into a coding agent in your app repository;
   the agent vendors the SDK from this server and mounts the widget. You then put the key in `.env.local` yourself.

See [examples/feedback-demo/README.md](examples/feedback-demo/README.md) for an SDK walkthrough, and
[docs/MANUAL_octobooks.ko.md](docs/MANUAL_octobooks.ko.md) for a full Korean walkthrough using a real pnpm monorepo
(register → tasks → review → release → in-app feedback widget).

## Build from source

```bash
pnpm install
make build          # web dashboard + bin/rhymehub + bin/rhymehub-supervisor
make test           # go vet + go test ./... + pnpm -r test
make e2e            # isolated end-to-end scenarios (temp RHYMEHUB_HOME, never touches ~/.rhymehub or launchd)
scripts/build-release.sh 0.1.0 out/   # release tarball for the host (GOOS/GOARCH to cross-compile)
```

## Feature status

### Verified in v0.1.0

Covered by automated tests (`make test`) and, where noted, by the end-to-end scenarios in
[docs/VALIDATION_REPORT.md](docs/VALIDATION_REPORT.md).

- Local users with roles (reporter/developer/maintainer/admin), sessions with CSRF protection, API tokens,
  per-project SDK keys, audit log.
- Projects: register by path, git URL clone (option-injection safe), discovery; test/build/release command
  policy; git status view.
- Claude Code / Codex CLI / fake adapters: detection, versions, capabilities, streaming, resume, cancel.
- Task state machine with durable event log, SSE streaming, follow-ups, questions, permission requests,
  cancel, retry, restart recovery.
- Scheduler: global / per-agent concurrency limits (persisted), priority, timeout, drain.
- Worktree per task, validation (tests/build) inside the worktree, diff review.
- Approval → merge → release with `none` / `tag` / `command` / `self` strategies; manual project release.
- SDK (TypeScript + React Feedback widget), `/sdk/*` endpoints, Inbox, request → task routing.
- Notifications: in-app feed, Web Push (VAPID) subscriptions, webhook, dedupe and retry.
- Supervisor: ed25519-signed update feed, SHA-256 verification, drain, health check, DB backup, automatic
  rollback, update journal recovery, history; launchd plist generation.
- Dashboard PWA: projects, tasks/chat, diff review, Inbox, releases, agents, system/update pages.

### Not implemented in v0.1 (planned)

- Kotlin/Java SDKs, multi-org tenancy, plugin marketplace.
- Distributed or multi-host execution and advanced resource optimisation.
- Web terminal.
- Deployment platform integrations beyond the `command` release strategy.
- `GET /system/updates` always reports `available: []`. Updates are applied from the configured feed through the
  supervisor; there is no in-dashboard update discovery yet.
- Windows.

### Known limitations

- The supervisor health-check timeout is fixed at 45 seconds.
- `POST /system/update` and `/system/rollback` return `202` immediately; check the update history for the outcome.
- See [docs/VALIDATION_REPORT.md](docs/VALIDATION_REPORT.md) for the scenario results and known issues.

## Security

Auth is always on. Request and app text is never executed as an OS command. Agents run with a scrubbed
environment, get no rhymehub credentials, and cannot read `$RHYMEHUB_HOME/secrets` or change approval and security
policy. Claude Code runs with `--setting-sources=` (no host user or repo `.claude` settings, hooks or allow
rules), and an approved permission request grants only a narrow rule such as `Bash(go test:*)`, never bare `Bash`
or a hard-denied command. See [docs/SECURITY.md](docs/SECURITY.md).

## Documentation

[Architecture](docs/ARCHITECTURE.md) · [API](docs/API_SPEC.md) · [Data model](docs/DATA_MODEL.md) ·
[Agent runtime](docs/AGENT_RUNTIME.md) · [SDK](docs/SDK_DESIGN.md) · [Security](docs/SECURITY.md) ·
[Release & update](docs/RELEASE_AND_UPDATE.md) · [ADRs](docs/adr)

## License

MIT. See [LICENSE](LICENSE).
