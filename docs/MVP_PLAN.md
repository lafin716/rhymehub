# MVP Plan

Detailed task status lives in `/PROGRESS.md` (single source of truth). Phases:

| Phase | Scope | Exit criteria |
|---|---|---|
| 1 Design | docs/*, ADRs, contracts, task graph | docs committed |
| 2 Platform | store+state machine, auth, projects, adapters (claude/codex/fake), worker, scheduler, worktrees, SSE, dashboard | Go tests green; scenario A works with fake + real agent |
| 3 SDK & requests | SDK, React Feedback, `/sdk/*`, Inbox, request→task routing, example app | SDK tests green; scenario B |
| 4 Release & self-update | approval gates, merge, release strategies, signing, feed, supervisor, rollback, web push | supervisor tests for all failure modes; scenarios C, D, E |
| 5 Integration & release | real-agent E2E A–E, independent reviews, docs, CI, GitHub public repo, v0.1.0 release | report in `docs/VALIDATION_REPORT.md` |

## Validation scenarios
* **A** dashboard task on a sample project → real Claude Code edits in worktree → tests → diff in dashboard.
* **B** example app Feedback UI → Inbox → auto task → real agent edits → status/result in app + dashboard.
* **C** Maintainer approves a passing task → merge → release (tag strategy) recorded.
* **D** self-improvement request on a clone of rhymehub → build + signed local release → supervisor update → dashboard reconnects on new version.
* **E** deliberately broken release (binary exits / health fails / corrupted package / migration failure) → supervisor rejects or rolls back, history recorded.

## Supervisor failure matrix (automated tests in services/supervisor)
normal update · corrupted package (sha mismatch) · bad signature · download failure · process exec failure ·
health check failure · migration failure (DB restored) · supervisor restart mid-update (journal recovery) · rollback.
