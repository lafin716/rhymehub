# ADR 0001 — Go core with pure-Go SQLite
Status: accepted (2026-11-19)
Context: must run on one Mac without Postgres/Redis; ship single binaries; cross-compile releases in CI.
Decision: Go for core/worker/supervisor; SQLite via modernc.org/sqlite (no cgo) in WAL mode with busy timeout,
shared by core and worker processes.
Consequences: simple install and cross-compilation; write concurrency limited (fine for single host);
multi-process access relies on WAL + short transactions.
