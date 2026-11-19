import type { RhymehubClientOptions } from "@rhymehub/react";

/** Vite env keys read by the demo. See .env.example. */
export interface RhymehubEnv {
  VITE_RHYMEHUB_PROJECT?: string;
  VITE_RHYMEHUB_KEY?: string;
  /** Used by the dev/preview `/api` proxy, not by the browser client. */
  VITE_RHYMEHUB_ENDPOINT?: string;
}

export type RhymehubConfig = { ok: true; options: RhymehubClientOptions } | { ok: false; missing: string[] };

/**
 * Build client options from env. The browser client always talks to same-origin `/api/v1`,
 * and the dev/preview server proxies `/api` to VITE_RHYMEHUB_ENDPOINT, so no CORS setup is needed.
 */
export function readRhymehubConfig(env: RhymehubEnv): RhymehubConfig {
  const projectId = env.VITE_RHYMEHUB_PROJECT?.trim() ?? "";
  const projectKey = env.VITE_RHYMEHUB_KEY?.trim() ?? "";
  const missing: string[] = [];
  if (projectId === "") missing.push("VITE_RHYMEHUB_PROJECT");
  if (projectKey === "") missing.push("VITE_RHYMEHUB_KEY");
  if (missing.length > 0) return { ok: false, missing };
  return {
    ok: true,
    options: {
      projectId,
      projectKey,
      endpoint: "",
      appVersion: "0.1.0",
      platform: "web",
    },
  };
}
