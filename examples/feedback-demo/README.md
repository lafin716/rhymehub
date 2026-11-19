# rhymehub feedback demo (DroidPad)

A fake app (counter and notes) that shows how to mount `RhymehubFeedback` from `@rhymehub/react`
in a Vite + React app. The feedback button sends requests to a local rhymehub core through the
dev/preview server's `/api` proxy, so the browser never talks to the core directly and needs no CORS setup.

Port: **5174**. The dev and preview servers bind `0.0.0.0` and allow `.ts.net` (Tailscale MagicDNS) and `localhost`.

## Setup

1. From the repo root: `pnpm install`
2. Create `examples/feedback-demo/.env.local` (git-ignored):

   ```bash
   VITE_RHYMEHUB_PROJECT=droidpad
   VITE_RHYMEHUB_KEY=rhk_your_project_reporter_key
   VITE_RHYMEHUB_ENDPOINT=http://127.0.0.1:7780
   ```

   `VITE_RHYMEHUB_ENDPOINT` is only used by the dev/preview proxy. It defaults to `http://127.0.0.1:7780`.
   Use a `rhk_` project reporter key created in the dashboard. Never use an `rht_` admin/CLI token,
   because Vite inlines `VITE_*` values into the browser bundle.

3. Start the core (`make build` at repo root, then run the rhymehub core on port 7780).
4. Run the demo:

   ```bash
   pnpm --filter @rhymehub/example-feedback-demo dev       # dev server
   pnpm --filter @rhymehub/example-feedback-demo build     # typecheck + production build
   pnpm --filter @rhymehub/example-feedback-demo preview   # serve dist/ on 0.0.0.0:5174
   pnpm --filter @rhymehub/example-feedback-demo test      # vitest (pure util tests)
   ```

Access URLs for the dev server, when running on a Mac with Tailscale:

- Local: `http://localhost:5174`
- LAN: `http://<lan-ip>:5174`
- Tailscale: `http://<hostname>.ts.net:5174` or `http://100.x.y.z:5174`

If `VITE_RHYMEHUB_PROJECT` or `VITE_RHYMEHUB_KEY` is missing, the app still renders, shows a notice,
and omits the feedback button.

## Files to read

- `src/config.ts`: reads the `VITE_RHYMEHUB_*` env into `RhymehubClientOptions`. The endpoint is `""`
  (same-origin `/api/v1`).
- `src/App.tsx`: `RhymehubProvider`, opt-in `client.diagnostics.captureConsole` and `captureErrors`
  (warn and error levels), and `RhymehubFeedback`.
- `vite.config.ts`: host, port, `allowedHosts`, and the `/api` proxy for both dev and preview.
- `src/lib/notes.ts`: pure state helpers with tests in `notes.test.ts`.

## Copy-out path (use this in your own app)

1. Install the packages: `pnpm add @rhymehub/react react react-dom`. `@rhymehub/react` pulls in `@rhymehub/sdk`.
2. Wrap the app in a provider with options that you read from your own config:

   ```tsx
   import { RhymehubProvider, RhymehubFeedback } from "@rhymehub/react";

   <RhymehubProvider options={{ projectId: "droidpad", endpoint: "", projectKey: import.meta.env.VITE_RHYMEHUB_KEY, appVersion: "1.2.3", platform: "web" }}>
     <App />
     <RhymehubFeedback />
   </RhymehubProvider>
   ```

3. Add a dev/preview `/api` proxy to your core, as in `vite.config.ts`, or set `endpoint` to the core origin
   if your core sends CORS headers for `Idempotency-Key` and `X-Rhymehub-Key`.
4. Copy `src/` as needed. The demo's `config.ts` and `lib/notes.ts` are not required by the SDK.

## Notes

- Attachments: the SDK supports screenshot and log attachments as base64 JSON. Multipart upload is not implemented
  because API_SPEC does not define it.
- Redaction runs in the SDK before data leaves the browser (description, title, diagnostics, answers, text attachments).
  Do not rely on it as the only protection for sensitive data.
- Diagnostics are off until `captureConsole` or `captureErrors` is called. Each upload is capped at 64 KiB, keeping the newest content.
