# rhymehub SDK + feedback widget install

Add the rhymehub SDK and its in-app feedback widget to this repository so users can send bug reports and feature
requests from inside the app to the rhymehub project `{{.ProjectID}}`. Make the smallest change that works and
match the repository's existing conventions (package manager, file layout, comment language, lint rules).

## Fixed values

- rhymehub server: `{{.ServerURL}}`
- rhymehub project id: `{{.ProjectID}}`
- SDK version: `{{.SDKVersion}}` (not on the npm registry; download the tarballs below from the rhymehub server)
{{- range .Packages}}
  - `{{.Name}}`: {{.URL}}
{{- end}}

## Hard rules

- Never ask for, read, print or write a real key. Only the user puts the project reporter key (`rhk_…`) into a
  git-ignored local env file. Never use an API token (`rht_…`) in app code; the SDK rejects it.
- Do not commit env files with values. Commit only an `.env.example` with empty key values.
- Do not touch unrelated code, do not upgrade other dependencies, do not run deploy or release commands.
- If the repository has uncommitted changes you did not make, leave them alone.

## Steps

1. **Detect the stack.** Find the app's web/UI package (in a monorepo, the package that renders the React root,
   usually the one with `createRoot(...)` and a `vite.config.*`). Detect the package manager from the lockfile
   (`pnpm-lock.yaml`, `package-lock.json`, `yarn.lock`, `bun.lockb`).
   - React 18+ → follow steps 2-7 (widget).
   - Other JS/TS frameworks → install only `@rhymehub/shared` and `@rhymehub/sdk`, create one small module that
     builds a `RhymehubClient` and exposes a "send feedback" function, wire it to an existing menu/help entry if
     one exists, then go to step 6.
   - Non-JS apps → do not install packages; add a short integration note based on the HTTP API
     (`POST {{.ServerURL}}/api/v1/sdk/requests` with header `X-Rhymehub-Key`) and stop after step 7.

2. **Vendor the tarballs.** Create `vendor/rhymehub/` at the repository root and download the {{len .Packages}} files
   above into it (for example `curl -fsSLo vendor/rhymehub/<file> <url>`). Commit them; they are MIT-licensed
   build inputs.

3. **Add the dependencies.** The SDK packages depend on each other by version, so point them at the tarballs:
   - pnpm: in the root `package.json` add
     `"pnpm": {"overrides": {"@rhymehub/sdk": "file:vendor/rhymehub/rhymehub-sdk-{{.SDKVersion}}.tgz", "@rhymehub/shared": "file:vendor/rhymehub/rhymehub-shared-{{.SDKVersion}}.tgz"}}`
     (merge with an existing `pnpm.overrides`).
   - npm: the same map under top-level `"overrides"`. yarn: under `"resolutions"`.
   - In the UI package add `"@rhymehub/react": "file:<relative path>/vendor/rhymehub/rhymehub-react-{{.SDKVersion}}.tgz"`
     to `dependencies`, then run the package manager's install so the lockfile updates.

4. **Add a wrapper component** next to the app entry, for example `src/rhymehub.tsx`:

   ```tsx
   import type { ReactNode } from 'react';
   import { RhymehubFeedback, RhymehubProvider, type RhymehubClientOptions } from '@rhymehub/react';

   // VITE_* values are bundled into the app: put only the project reporter key (rhk_) here, never an API token.
   const projectId = (import.meta.env.VITE_RHYMEHUB_PROJECT as string | undefined)?.trim();
   const projectKey = (import.meta.env.VITE_RHYMEHUB_KEY as string | undefined)?.trim();
   // Empty endpoint = same origin; the dev server proxies /api to rhymehub.
   const endpoint = (import.meta.env.VITE_RHYMEHUB_PUBLIC_URL as string | undefined)?.trim() ?? '';

   export function rhymehubOptions(): RhymehubClientOptions | null {
     if (!projectId || !projectKey) return null;
     return { projectId, projectKey, endpoint, platform: 'web' };
   }

   // Without a key the app renders unchanged, so builds without rhymehub keep working.
   export function WithRhymehub({ children }: { children: ReactNode }) {
     const options = rhymehubOptions();
     if (!options) return <>{children}</>;
     return (
       <RhymehubProvider options={options}>
         {children}
         <RhymehubFeedback />
       </RhymehubProvider>
     );
   }
   ```

   Pass `appVersion` too if the app already exposes its version. For non-Vite bundlers use that bundler's public
   env convention (for example `process.env.NEXT_PUBLIC_*`) instead of `import.meta.env.VITE_*`.

5. **Mount it.** Wrap the main app element where the React root renders (`<WithRhymehub><App /></WithRhymehub>`).
   Skip secondary windows such as presenter, popup or print views.

6. **Route requests to rhymehub.**
   - Vite: in `vite.config.*` use `loadEnv` and add
     `proxy: { '/api': { target: env.VITE_RHYMEHUB_ENDPOINT || '{{.ServerURL}}', changeOrigin: true } }` to both
     `server` and `preview`. If the app already proxies `/api` to its own backend, do not replace it: leave the
     proxy out and tell the user to set `VITE_RHYMEHUB_PUBLIC_URL` and the key's allowed origins instead.
   - Keep any existing `server.host` / `allowedHosts` settings.

7. **Env files.**
   - Create `.env.example` in the UI package:
     ```
     VITE_RHYMEHUB_PROJECT={{.ProjectID}}
     VITE_RHYMEHUB_KEY=
     VITE_RHYMEHUB_ENDPOINT={{.ServerURL}}
     VITE_RHYMEHUB_PUBLIC_URL=
     ```
   - Make sure `.gitignore` ignores `.env.local` and `*.local`.

8. **Verify.** Run the repository's existing typecheck, lint and test commands for the touched packages and fix
   anything you broke. Do not start long-running servers.

9. **Report** the changed files and the commands you ran, then tell the user to:
   1. create a project reporter key in the rhymehub dashboard (Project `{{.ProjectID}}` → SDK keys);
   2. copy `.env.example` to `.env.local` in the UI package and paste the key into `VITE_RHYMEHUB_KEY`;
   3. restart the dev server and look for the feedback button in the bottom corner.
