import { RhymehubError } from "./errors";
import { defaultStorage, type KeyValueStorage } from "./storage";
import { HttpClient } from "./http";
import { RequestsApi } from "./requests";
import { ReleasesApi } from "./releases";
import { DiagnosticsCollector } from "./diagnostics";
import { UUID_PATTERN, randomUuid } from "./encoding";

/** localStorage key holding the random per-install id. */
export const INSTALLATION_ID_STORAGE_KEY = "rhymehub.installationId";

export interface RhymehubClientOptions {
  /** Project slug, e.g. "droidpad". Informational; the server derives the project from the key. */
  projectId: string;
  /** Origin of the rhymehub core, e.g. `https://mac.tailnet.ts.net:7780`. Use `""` for same-origin (dev proxy). */
  endpoint: string;
  /** Project reporter key (`rhk_...`). Never an admin or CLI token (`rht_...`). */
  projectKey: string;
  appVersion?: string;
  platform?: string;
  /** Installation-id persistence. Default: localStorage when available, else memory. */
  storage?: KeyValueStorage;
  /** Custom fetch (tests, proxies). Default: global fetch. */
  fetch?: typeof fetch;
  retry?: {
    /** Extra attempts for idempotent calls (GET, create with Idempotency-Key). Default 2. */
    attempts?: number;
    /** Base backoff; doubles per attempt. Default 300 ms. */
    baseDelayMs?: number;
  };
}

/** Throws for missing or admin-class credentials. Returns the key when acceptable. */
export function assertReporterKey(key: unknown): string {
  if (typeof key !== "string" || key.trim() === "") {
    throw new RhymehubError(0, "invalid_key", "projectKey is required: a project reporter key (rhk_...)");
  }
  const trimmed = key.trim();
  if (trimmed.startsWith("rht_")) {
    throw new RhymehubError(
      0,
      "invalid_key",
      "rht_ tokens are admin/CLI API tokens and must never be shipped in an app. " +
        "Create a project reporter key (rhk_...) in the dashboard for this project instead.",
    );
  }
  if (!trimmed.startsWith("rhk_")) {
    throw new RhymehubError(0, "invalid_key", "projectKey must be a project reporter key (rhk_...)");
  }
  return trimmed;
}

function normalizeEndpoint(endpoint: string): string {
  let value = (endpoint ?? "").trim().replace(/\/+$/, "");
  if (value.endsWith("/api/v1")) value = value.slice(0, -"/api/v1".length);
  if (value !== "" && !/^https?:\/\//i.test(value)) {
    throw new RhymehubError(0, "invalid_config", "endpoint must be an http(s) origin or empty for same-origin");
  }
  return value;
}

export class RhymehubClient {
  readonly projectId: string;
  readonly appVersion?: string;
  readonly platform?: string;
  readonly requests: RequestsApi;
  readonly releases: ReleasesApi;
  /** Opt-in console / error capture. Nothing is captured until you call `captureConsole` / `captureErrors`. */
  readonly diagnostics: DiagnosticsCollector;

  private readonly storage: KeyValueStorage;
  private cachedInstallationId?: string;

  constructor(options: RhymehubClientOptions) {
    if (!options || typeof options.projectId !== "string" || options.projectId.trim() === "") {
      throw new RhymehubError(0, "invalid_config", "projectId is required");
    }
    const projectKey = assertReporterKey(options.projectKey);
    const fetchImpl = options.fetch ?? (typeof globalThis.fetch === "function" ? globalThis.fetch.bind(globalThis) : undefined);
    if (!fetchImpl) {
      throw new RhymehubError(0, "invalid_config", "no fetch implementation available; pass options.fetch");
    }

    this.projectId = options.projectId;
    this.appVersion = options.appVersion;
    this.platform = options.platform;
    this.storage = options.storage ?? defaultStorage();
    this.diagnostics = new DiagnosticsCollector();

    const http = new HttpClient({
      endpoint: normalizeEndpoint(options.endpoint),
      projectKey,
      fetch: fetchImpl,
      retry: {
        attempts: Math.max(0, options.retry?.attempts ?? 2),
        baseDelayMs: Math.max(0, options.retry?.baseDelayMs ?? 300),
      },
    });
    const installationId = () => this.installationId;
    this.requests = new RequestsApi({
      http,
      installationId,
      appVersion: this.appVersion,
      platform: this.platform,
    });
    this.releases = new ReleasesApi({ http, appVersion: this.appVersion, platform: this.platform });
  }

  /** Random UUID persisted under `rhymehub.installationId`. Stable across reloads on the same storage. */
  get installationId(): string {
    if (this.cachedInstallationId) return this.cachedInstallationId;
    let id: string | null = null;
    try {
      id = this.storage.getItem(INSTALLATION_ID_STORAGE_KEY);
    } catch {
      id = null;
    }
    if (!id || !UUID_PATTERN.test(id)) {
      id = randomUuid();
      try {
        this.storage.setItem(INSTALLATION_ID_STORAGE_KEY, id);
      } catch {
        // storage unavailable: id lives for this client only
      }
    }
    this.cachedInstallationId = id;
    return id;
  }
}
