# ADR 0002 — Detached worker process per task
Status: accepted
Context: core restarts (including self-update) must not lose running agent work.
Decision: core spawns `rhymehub worker --task ID` in its own session; the worker persists events/state in SQLite
and heartbeats. Core re-attaches by pid+heartbeat or recovers orphans (RETRYING→QUEUED with session resume).
Alternatives: in-process goroutines (lost on restart); separate long-lived worker daemon (more moving parts).
