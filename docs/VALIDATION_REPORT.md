# rhymehub v0.1.0 — Validation Report

Date: 2026-11-19 · Host: macOS (Darwin 25.1, arm64) · Go 1.27.1 · pnpm workspace · real Claude Code CLI

All scenario runs use an isolated temporary `RHYMEHUB_HOME`, loopback ports 17790–17792, and never touch
`~/.rhymehub` or `launchctl`.

## 1. Automated tests (`make test`)

| Suite | Result |
|---|---|
| `go build ./...`, `go vet ./...` | clean |
| `go test ./...` | 378 tests, 20 packages, all pass |
| `pnpm -r test` (vitest) | shared 11, web 43, sdk-typescript 18, sdk-react 7, feedback-demo 9, all pass |
| `make test` | exit 0 |

## 2. End-to-end scenarios

### A–C: real Claude Code agent (`scripts/e2e/scenarios-abc.sh`)

The fixture is `scripts/e2e/fixtures/sample-go`, a Go module whose `Mul` is deliberately missing, so its
tests fail before the agent runs. A scripted reviewer approves agent permission requests through
`POST /tasks/{id}/permission`, at most 3 times per task.

| Scenario | Flow | Result |
|---|---|---|
| A | API task → agent edits in worktree → tests pass in worktree → REVIEW_READY with diff touching `calc.go` | PASS (2 turns, $0.067) |
| B | SDK key → SDK request → Inbox → task → REVIEW_READY; SDK view shows progress; other installation gets 404 | PASS (2 turns, $0.079) |
| C | Project `releaseStrategy=tag` → approve with release → merge to main → git tag → release listed; main tests pass | PASS |

The final run (run3) scored **37 PASS / 0 FAIL**, with an agent runtime of 42s.

#### Product defects found by these scenarios and fixed

1. **Permission grant was unusable.** Run1 scored 21 PASS / 16 FAIL, with every task stuck in WAITING_APPROVAL.
   - Cause: approving a Bash denial granted bare `Bash`, which the adapter correctly rejects as too broad.
   - Fix: `adapters.GrantRule` derives a narrow rule (`Bash(go test:*)`) from the denied command.
   - Rules that are too broad or hard-denied (`*`, `Bash`, `curl`, `sudo`, `git push`, WebFetch…) are refused.
   - Project `allowedTools` validation rejects the same rules.
   - Test: `apps/server/internal/api/permission_test.go`.
2. **Agent inherited the host user's Claude settings.** Run2 scored 26 PASS / 11 FAIL.
   - Cause: the child `claude` process loaded the operator's `~/.claude` hooks, which rewrite commands, so the granted rules never matched.
   - This was also a policy bypass. Host allow rules, or a worktree `.claude/settings.json` that the agent itself could edit, would widen rhymehub's permissions.
   - Fix: the Claude adapter now passes `--setting-sources=`. Official CLI auth still works; this was verified with a real call.

### D–E: self-update and rollback (`scripts/e2e/selfupdate.sh`)

The test signs real binaries with a test ed25519 key and serves them from a local feed. The supervisor manages the core.

| Scenario | Result |
|---|---|
| D: update 0.0.1-e2e → 0.0.2-e2e (drain, backup, switch, health, history) | PASS |
| E1: new build exits on serve → automatic rollback | PASS |
| E2: new build hangs (health check fails) → rollback | PASS |
| E3: corrupted artifact → rejected before switch (SHA-256) | PASS |
| E4: bad signature → rejected before switch | PASS |
| E5: migration failure (exit 3) → DB restored, rollback | PASS |

Total: **52 PASS / 0 FAIL**.

## 3. Independent security review: findings fixed

| Severity | Finding | Fix |
|---|---|---|
| MEDIUM | git subprocesses inherited the host environment | `workspace.GitEnv` scrubs the env for git and validation |
| MEDIUM | password reset or disable left sessions alive | sessions are revoked (`DeleteUserSessions`) |
| MEDIUM | developer could change `buildCmd` (command policy) | `buildCmd` is maintainer-gated like test and release commands |
| HIGH (e2e) | permission grant/host settings, as above | narrow grants and `--setting-sources=` |

## 4. Tested vs. not implemented

- **Tested:** everything under "Verified in v0.1.0" in [README](../README.md#verified-in-v010).
  - Agent flows (A–C) were exercised with Claude Code.
  - Codex is covered by adapter unit tests and CLI probes only, not by a live e2e run.
- **Not implemented:**
  - Kotlin/Java SDKs.
  - Multi-org tenancy.
  - Multi-host execution.
  - Web terminal.
  - Deployment integrations beyond the `command` strategy.
  - In-dashboard update discovery (`/system/updates` returns `available: []`).
  - Windows.

## 5. Known issues

- API tokens are not revoked when a user's password is reset or the user is disabled. Only sessions are revoked.
- Project discovery lets a developer enumerate directories under the configured roots.
- The login rate limit is keyed globally when behind a reverse proxy, since `X-Forwarded-For` is not trusted.
- First-admin setup has a small race window: two concurrent setup calls may both pass the "no users" check.
- The supervisor health-check timeout is fixed at 45s.
- `POST /system/update` and `/system/rollback` return 202. The outcome appears in the update history.
- A stale `core.pid` cleanup prints a harmless stderr line.
- The Codex adapter still loads the host `~/.codex/config.toml`. There is no equivalent isolation flag yet.
- The forbidden-tool fragment list is conservative and static.
