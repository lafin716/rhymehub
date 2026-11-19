# ADR 0003 — Agent adapters use only official non-interactive CLI interfaces
Status: accepted
Decision: Claude Code via `claude -p --output-format stream-json` (+ --session-id/--resume, permission flags);
Codex via `codex exec --json` (+ `exec resume`). Capabilities are declared per adapter and the UI adapts
(e.g. permission requests are only surfaced where the CLI reports denials). No token extraction, no API reuse of
subscription credentials. A deterministic `fake` adapter enables tests without credentials.
