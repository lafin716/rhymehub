# Product Requirements — rhymehub

rhymehub (spec name “DevHub”) is an open-source, self-hosted, web-based AI development and application
lifecycle platform. It runs on one MacBook and is used from mobile/desktop browsers (PWA).

## Goals (priority order)
1. Manage many projects from one mobile-friendly dashboard.
2. Run and converse with Claude Code / Codex CLI per project, with streaming output.
3. Let managed apps create development requests in-app via a common SDK.
4. Route requests to the project's git repo/source and run the agent in an isolated worktree.
5. Analysis → change → tests/build → review inside the worktree.
6. Developers follow progress and approve from mobile.
7. Release approved changes; apps can check for updates.
8. rhymehub develops and updates itself (self project + Supervisor).
9. Easy onboarding: SDK, CLI (`init/register/doctor/status`), templates, skills.

## Personas / roles
App Reporter (in-app requests), Developer (manages requests, runs tasks), Maintainer (merge + release approval,
policies), Administrator (users, system, self-update).

## Functional requirements (MVP)
| ID | Requirement |
|---|---|
| FR-1 | Local install on macOS; `rhymehub init` creates home, admin user, keys; `serve` runs the dashboard. |
| FR-2 | Login (local users, roles), API tokens, project SDK keys. |
| FR-3 | Register projects by local path, git URL clone, or discovery; self project auto-registered. |
| FR-4 | Detect Claude Code / Codex CLI, show versions and capabilities. |
| FR-5 | Per-project AI chat: create task, stream events, follow-up messages, answer questions, grant permissions, cancel, retry. |
| FR-6 | Task per git worktree + branch; parallel tasks; conflict detection at merge. |
| FR-7 | Scheduler: global / per-project / per-agent limits, priority, retry, timeout, restart recovery. |
| FR-8 | Unified Inbox for requests from dashboard / SDK / CLI with status, priority, linked task. |
| FR-9 | SDK request → Inbox → task → agent edits → tests → result visible in app and dashboard. |
| FR-10 | Diff and test result review; Maintainer approval; merge; release strategies none/tag/command/self. |
| FR-11 | Notifications: in-app list + Web Push (VAPID) + webhook; dedupe; retry. |
| FR-12 | Supervisor: verified self-update, drain, health check, rollback, DB backup, history; launchd plist. |
| FR-13 | Audit log of security-relevant actions. |

## Non-functional
* No external infra; single binaries; SQLite.
* Mobile-first responsive UI; task approval, diff review and chat usable on a phone.
* Safe defaults for open-source installs (auth always on, TLS policy, both auto policies off).
* State survives core restarts; event log is durable.

## Out of scope for v0.1
Kotlin/Java SDKs, multi-org tenancy, plugin marketplace, distributed/multi-host execution, advanced resource
optimisation, many deployment platform integrations, web terminal (planned, separate from task UI).
