# Web dashboard API assumptions (WEB-1 handoff to CORE-2)

The dashboard (apps/web) was built against API_SPEC and assumes the following. CORE-2 must either
implement these or report a deviation so the leader can adjust apps/web.

1. `POST /api/v1/setup` does not create a session; the client calls `POST /api/v1/auth/login` afterwards.
2. List endpoints may return a bare array or a keyed envelope (`unwrapList` accepts both).
3. Rollback: `POST /api/v1/system/rollback` with body `{updateId}` (not in API_SPEC; to be added).
4. API token list fields follow `ApiToken` in `apps/web/src/api/types.ts`.
5. SSE `task_updated` payload is a `TaskSummary`.
6. SSE event names: `task_event`, `task_updated`, `request_updated`, `notification`, `system`.
7. Audit log section is admin-only.
8. Event backfill: `GET /api/v1/tasks/{id}/events?afterSeq=N&limit=M`.

Known gaps: PWA icons are SVG only (no 192/512 PNG); `<Select className="w-auto">` overrides default classes.
