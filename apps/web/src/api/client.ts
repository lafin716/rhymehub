import type { ApiErrorBody, ApiErrorCode } from './types';

export const API_BASE = '/api/v1';
export const CSRF_HEADER = 'X-Rhymehub-CSRF';
export const CSRF_VALUE = '1';

/** Error thrown for every non-2xx API response (or network failure, status 0). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ApiErrorCode | 'network' | 'unknown';

  constructor(status: number, code: ApiError['code'], message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

type UnauthorizedHandler = () => void;

let unauthorizedHandler: UnauthorizedHandler | null = null;

/**
 * Registered by the auth provider. Called on any 401 except the login/setup probes,
 * so the UI can move to the login screen.
 */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  unauthorizedHandler = handler;
}

const AUTH_PROBE_PATHS = ['/auth/login', '/auth/me', '/setup', '/meta'];

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'DELETE' | 'PUT';
  /** Query parameters; undefined/null/'' values are skipped. */
  query?: Record<string, string | number | boolean | undefined | null>;
  body?: unknown;
  signal?: AbortSignal;
}

/** Builds `/api/v1` + path + query string. Paths start with `/`, e.g. `/tasks/abc`. */
export function buildUrl(path: string, query?: RequestOptions['query']): string {
  let url = path.startsWith('/healthz') ? path : `${API_BASE}${path}`;
  if (query) {
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(query)) {
      if (value === undefined || value === null || value === '') continue;
      params.set(key, String(value));
    }
    const qs = params.toString();
    if (qs) url += `?${qs}`;
  }
  return url;
}

async function readErrorBody(response: Response): Promise<ApiErrorBody['error'] | null> {
  try {
    const parsed = (await response.json()) as Partial<ApiErrorBody>;
    if (parsed && typeof parsed === 'object' && parsed.error && typeof parsed.error.message === 'string') {
      return parsed.error;
    }
  } catch {
    // Non-JSON body (proxy error page, etc.) falls through to the generic message.
  }
  return null;
}

/**
 * Low-level request. Sends cookies, adds the CSRF header on state-changing methods,
 * parses JSON, and maps the `{error:{code,message}}` envelope to ApiError.
 */
export async function apiRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? 'GET';
  const headers: Record<string, string> = { Accept: 'application/json' };
  let body: BodyInit | undefined;

  if (method !== 'GET') {
    headers[CSRF_HEADER] = CSRF_VALUE;
  }
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(options.body);
  }

  let response: Response;
  try {
    response = await fetch(buildUrl(path, options.query), {
      method,
      headers,
      body,
      credentials: 'same-origin',
      signal: options.signal,
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, 'network', 'Cannot reach rhymehub. Check the connection and try again.');
  }

  if (response.status === 204) {
    return undefined as T;
  }

  if (!response.ok) {
    const envelope = await readErrorBody(response);
    const code = (envelope?.code as ApiErrorCode | undefined) ?? codeFromStatus(response.status);
    const message = envelope?.message ?? (response.statusText || `Request failed (${response.status})`);
    if (response.status === 401 && unauthorizedHandler && !AUTH_PROBE_PATHS.some((p) => path.startsWith(p))) {
      unauthorizedHandler();
    }
    throw new ApiError(response.status, code, message);
  }

  const text = await response.text();
  if (!text) return undefined as T;
  return JSON.parse(text) as T;
}

function codeFromStatus(status: number): ApiError['code'] {
  switch (status) {
    case 400:
      return 'bad_request';
    case 401:
      return 'unauthorized';
    case 403:
      return 'forbidden';
    case 404:
      return 'not_found';
    case 409:
      return 'conflict';
    case 429:
      return 'rate_limited';
    default:
      return status >= 500 ? 'internal' : 'unknown';
  }
}

/**
 * List responses may be a bare array or wrapped under a key (e.g. `{projects:[...]}`).
 * Accepting both keeps the client tolerant while the core's list envelopes settle.
 */
export function unwrapList<T>(body: unknown, key: string): T[] {
  if (Array.isArray(body)) return body as T[];
  if (body && typeof body === 'object') {
    const value = (body as Record<string, unknown>)[key];
    if (Array.isArray(value)) return value as T[];
  }
  return [];
}

export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return 'Something went wrong.';
}
