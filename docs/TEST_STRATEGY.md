# Test Strategy

| Layer | Tooling | Scope |
|---|---|---|
| Go unit | `go test ./...` | state machine, store (temp SQLite), redact, signing, workspace (temp git repos), adapters (parse recorded JSONL fixtures from real CLI runs), scheduler limits/recovery, release versioning, supervisor phases |
| Go integration | `go test ./apps/server/...` with `httptest` + fake adapter + real git | API auth/RBAC/TLS policy, request→task→worker→REVIEW_READY→approve→merge→release, SSE |
| Supervisor | `go test ./services/supervisor/...` building stub core binaries in temp dirs | full failure matrix (MVP_PLAN) |
| TS | `vitest` | SDK client (mock fetch), redact parity, React Feedback (testing-library, jsdom), web utils |
| Web build | `pnpm -r build`, `tsc --noEmit` | type safety |
| E2E (real) | `scripts/e2e/*.sh` + Playwright screenshots | scenarios A–E with real Claude Code CLI on this Mac; evidence in `docs/VALIDATION_REPORT.md` |
| CI | GitHub Actions | Go vet/test, pnpm test/build (fake adapter only; no model credentials in CI) |

Commands: `make test` (= `go test ./... && pnpm -r test`), `make build`, `make e2e`.
Real-agent tests are never run in CI; they are run locally and their outputs recorded.
