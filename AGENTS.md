# rhymehub — Agent Instructions

## Sources and commands
- Read PROGRESS.md, docs/MASTER_PLAN.md, docs/ARCHITECTURE.md and the task-specific docs (DATA_MODEL, API_SPEC,
  AGENT_RUNTIME, SDK_DESIGN, SECURITY, RELEASE_AND_UPDATE) before work. These docs are the contracts.
- Go: `go build ./... && go vet ./... && go test ./...` (Go module at repo root, `github.com/lafin716/rhymehub`).
- JS: `pnpm install`, `pnpm -r test`, `pnpm -r build` (pnpm workspace at repo root).
- Full: `make build`, `make test`.
- Dev servers bind `0.0.0.0`; Vite `server.allowedHosts`/`preview.allowedHosts` include `.ts.net`.
- Never touch the user's real `~/.rhymehub` or load launchd jobs in tests; use temp `RHYMEHUB_HOME`.

## Ownership and handoff
- Follow the leader's Task IDs and exclusive file ownership; do not revert others' work.
- Shared contracts (docs/*), go.mod/go.sum, root package.json/pnpm-workspace.yaml, Makefile and PROGRESS.md are
  leader-owned. If you need a new dependency, report it instead of editing go.mod (pnpm package.json inside
  your own package dir is yours).
- Report: changed files, commands + results, blockers, contract deviations. No recursive delegation.
- DONE requires passing validation. Secrets never go into tracked files or logs.

## Security invariants (never violate)
- Never execute request/app text as OS commands; only configured test/build/release commands run.
- Never pass credentials/tokens to agents; scrub env; deny agents access to $RHYMEHUB_HOME/secrets.
- Agents cannot change approval/security policy. Auto-approve and auto-release are separate and default off.
- Use only official CLI auth for claude/codex; never read their credential stores.
