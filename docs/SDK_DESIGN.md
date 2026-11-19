# SDK Design

Two npm packages in the pnpm workspace (TypeScript, ESM + d.ts, built with `tsup`, tested with `vitest`).

## `@rhymehub/sdk` (headless, `packages/sdk-typescript`)

```ts
import { RhymehubClient } from "@rhymehub/sdk";

const rh = new RhymehubClient({
  projectId: "droidpad",
  endpoint: "https://mac.tailnet.ts.net:7780",
  projectKey: import.meta.env.VITE_RHYMEHUB_KEY,  // reporter key (rhk_...), never an admin token
  appVersion: "1.4.2", platform: "web",            // optional
  storage?: KeyValueStorage,                       // installationId persistence (default localStorage / memory)
  fetch?: typeof fetch,
});

const req = await rh.requests.create({ type: "improvement", description: "블루투스 자동 재연결 기능 추가",
  diagnostics: [{ kind: "log", name: "console", content: logs }], attachments: [{ kind: "screenshot", file }] });
await rh.requests.list();              // this installation's requests
await rh.requests.get(req.id);
await rh.requests.answer(req.id, "Android 14, Pixel 8");
const stop = rh.requests.watch(req.id, (r) => render(r), { intervalMs: 3000 }); // polling, returns unsubscribe
await rh.releases.checkForUpdate();    // {updateAvailable, latestVersion, notes, update:{kind,url}}
rh.diagnostics.captureConsole({ max: 200 }); rh.diagnostics.captureErrors();    // opt-in ring buffers
```

* Types mirror `SdkRequest` from API_SPEC (exported from `@rhymehub/shared` and re-exported).
* Client-side scrubbing: `redact()` applies the same patterns as the server before upload (defence in depth;
  server redaction remains authoritative).
* Errors: `RhymehubError {status, code, message}`. Retries: idempotent GETs retried with backoff; POST create
  sends an `Idempotency-Key` header (server dedupes within 24 h per key).
* Installation id: random UUID persisted under `rhymehub.installationId`.
* `progress` mapping is computed server-side so all SDKs agree.

## `@rhymehub/react` (`packages/sdk-react`)

```tsx
import { RhymehubProvider, RhymehubFeedback, useRhymehubRequests } from "@rhymehub/react";

<RhymehubFeedback projectId="droidpad" endpoint="..." projectKey="..." />           // self-contained
<RhymehubProvider client={rh}><RhymehubFeedback /></RhymehubProvider>             // shared client
```

UI: floating button (position configurable) → panel with tabs **New request** (type select, description,
attach screenshot via file input / `captureScreenshot` hook, include logs toggle) and **My requests** (status chips,
progress bar, agent question + answer box, result summary, release status, “Check for updates”). Styles are
self-contained (CSS-in-JS via inline style objects + a scoped class prefix `rh-`) so the host app needs no
Tailwind. Headless hooks: `useRhymehubClient`, `useRhymehubRequests`, `useRhymehubRequest(id)`,
`useRhymehubUpdate`. Accessible: dialog role, focus trap, Escape to close, labels.

## Future SDKs
Kotlin/Java SDKs will implement the same `/api/v1/sdk/*` protocol (documented in API_SPEC). Not shipped in v0.1.
