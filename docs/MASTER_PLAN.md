# rhymehub Master Plan

Goal: ship rhymehub v0.1.0 — a self-hosted AI development platform (spec "DevHub") — as a public GitHub repo
with a published release, after validating scenarios A–E for real on this Mac.

Completion criteria: all PROGRESS tasks DONE with PASS evidence; `make test` green; scenarios A–E documented in
`docs/VALIDATION_REPORT.md`; public repo `lafin716/rhymehub`; GitHub Release v0.1.0 with darwin/linux binaries
+ SHA256SUMS; tested vs. not-implemented features clearly separated in README/report.

Design docs: PRODUCT_REQUIREMENTS, ARCHITECTURE, DATA_MODEL, API_SPEC, AGENT_RUNTIME, SDK_DESIGN, SECURITY,
RELEASE_AND_UPDATE, MVP_PLAN, TEST_STRATEGY, adr/.

Execution: leader owns contracts/docs/PROGRESS/integration; implementation in waves with exclusive ownership:
* Wave 1 (parallel): foundation (domain/store/config/redact/workspace), agent adapters, supervisor+signing,
  web dashboard, SDK+React+shared+example.
* Wave 2: core API/auth/scheduler/notify/CLI + worker + release manager (depends on foundation, adapters).
* Wave 3: integration, independent reviews (auth/security, state machine/scheduler, supervisor), real E2E A–E.
* Wave 4: CI, docs, GitHub publish, release.

Risks: real-agent cost/time (use small sample projects, haiku/sonnet models); multi-process SQLite locking
(WAL + busy_timeout + short tx); self-update test must not touch the user's real ~/.rhymehub or launchd
(use isolated RHYMEHUB_HOME and foreground supervisor; plist only generated + linted).
