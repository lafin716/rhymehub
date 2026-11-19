import type { ApiErrorBody } from "@rhymehub/shared";
import { RhymehubError } from "./errors";

export interface HttpConfig {
  /** Origin of the rhymehub core without `/api/v1`. Empty string = same origin. */
  endpoint: string;
  projectKey: string;
  fetch: typeof fetch;
  retry: { attempts: number; baseDelayMs: number };
}

export interface HttpCall {
  method: "GET" | "POST";
  path: string;
  query?: Record<string, string | undefined>;
  body?: unknown;
  headers?: Record<string, string>;
  /** Extra attempts after the first one for retryable failures (network, 429, 5xx). */
  retries: number;
}

type AttemptOutcome<T> =
  | { ok: true; value: T }
  | { ok: false; error: RhymehubError; retryable: boolean };

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function readApiError(res: Response): Promise<RhymehubError> {
  let code = "http_error";
  let message = res.statusText || `HTTP ${res.status}`;
  try {
    const parsed = JSON.parse(await res.text()) as Partial<ApiErrorBody>;
    if (parsed && parsed.error) {
      code = parsed.error.code || code;
      message = parsed.error.message || message;
    }
  } catch {
    // non-JSON error body: keep the status text
  }
  return new RhymehubError(res.status, code, message);
}

export class HttpClient {
  constructor(private readonly config: HttpConfig) {}

  url(path: string, query?: Record<string, string | undefined>): string {
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(query ?? {})) {
      if (v !== undefined) params.set(k, v);
    }
    const qs = params.toString();
    return `${this.config.endpoint}/api/v1${path}${qs ? `?${qs}` : ""}`;
  }

  async request<T>(call: HttpCall): Promise<T> {
    const url = this.url(call.path, call.query);
    for (let attempt = 0; ; attempt++) {
      const outcome = await this.attempt<T>(call, url);
      if (outcome.ok) return outcome.value;
      if (!outcome.retryable || attempt >= call.retries) throw outcome.error;
      await sleep(this.backoffMs(attempt));
    }
  }

  private backoffMs(attempt: number): number {
    const base = this.config.retry.baseDelayMs * 2 ** attempt;
    // +/-20% jitter so many clients do not retry in lockstep.
    return Math.round(base * (0.8 + Math.random() * 0.4));
  }

  private async attempt<T>(call: HttpCall, url: string): Promise<AttemptOutcome<T>> {
    const headers: Record<string, string> = {
      Accept: "application/json",
      "X-Rhymehub-Key": this.config.projectKey,
      ...call.headers,
    };
    let body: string | undefined;
    if (call.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(call.body);
    }

    let res: Response;
    try {
      res = await this.config.fetch(url, { method: call.method, headers, body });
    } catch (err) {
      const reason = err instanceof Error ? err.message : String(err);
      return {
        ok: false,
        error: new RhymehubError(0, "network_error", `Could not reach rhymehub: ${reason}`),
        retryable: true,
      };
    }

    if (!res.ok) {
      const error = await readApiError(res);
      return { ok: false, error, retryable: res.status === 429 || res.status >= 500 };
    }
    if (res.status === 204) return { ok: true, value: undefined as T };
    try {
      return { ok: true, value: (await res.json()) as T };
    } catch {
      return {
        ok: false,
        error: new RhymehubError(res.status, "bad_response", "rhymehub returned a non-JSON response"),
        retryable: false,
      };
    }
  }
}
