# ADR 0005 — SSE over a durable event log
Status: accepted
Decision: task_events in SQLite are the source; core tails the table (300 ms) and streams via SSE with
Last-Event-ID replay. WebSocket deferred (only needed for a web terminal).
