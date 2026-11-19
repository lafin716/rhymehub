import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, apiRequest, buildUrl, errorMessage, setUnauthorizedHandler, unwrapList } from './client';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('apiRequest', () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    setUnauthorizedHandler(null);
  });

  it('adds the CSRF header on non-GET requests only', async () => {
    fetchMock.mockImplementation(async () => jsonResponse(200, { ok: true }));

    await apiRequest('/projects');
    await apiRequest('/projects', { method: 'POST', body: { name: 'x' } });

    const [getUrl, getInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    const [, postInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(getUrl).toBe('/api/v1/projects');
    expect((getInit.headers as Record<string, string>)['X-Rhymehub-CSRF']).toBeUndefined();
    expect((postInit.headers as Record<string, string>)['X-Rhymehub-CSRF']).toBe('1');
    expect(postInit.body).toBe(JSON.stringify({ name: 'x' }));
  });

  it('maps the error envelope to ApiError with code and message', async () => {
    fetchMock.mockResolvedValue(jsonResponse(403, { error: { code: 'forbidden', message: 'Not allowed.' } }));

    const err = await apiRequest('/users').catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 403, code: 'forbidden', message: 'Not allowed.' });
    expect(errorMessage(err)).toBe('Not allowed.');
  });

  it('falls back to a status-derived code when the body is not an envelope', async () => {
    fetchMock.mockResolvedValue(new Response('<html>bad gateway</html>', { status: 502 }));

    const err = await apiRequest('/tasks').catch((e: unknown) => e);

    expect(err).toMatchObject({ status: 502, code: 'internal' });
  });

  it('calls the unauthorized handler on 401 for ordinary paths', async () => {
    const handler = vi.fn();
    setUnauthorizedHandler(handler);
    fetchMock.mockResolvedValue(jsonResponse(401, { error: { code: 'unauthorized', message: 'Sign in.' } }));

    await expect(apiRequest('/tasks')).rejects.toMatchObject({ status: 401 });
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it('does not call the unauthorized handler for the /auth/me probe', async () => {
    const handler = vi.fn();
    setUnauthorizedHandler(handler);
    fetchMock.mockResolvedValue(jsonResponse(401, { error: { code: 'unauthorized', message: 'Sign in.' } }));

    await expect(apiRequest('/auth/me')).rejects.toMatchObject({ status: 401 });
    expect(handler).not.toHaveBeenCalled();
  });

  it('maps a network failure to status 0 with code network', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));

    const err = await apiRequest('/tasks').catch((e: unknown) => e);

    expect(err).toMatchObject({ status: 0, code: 'network' });
  });

  it('returns undefined for 204 responses', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await expect(apiRequest('/tasks/1', { method: 'DELETE' })).resolves.toBeUndefined();
  });
});

describe('buildUrl', () => {
  it('prefixes API paths and leaves /healthz alone', () => {
    expect(buildUrl('/tasks/abc')).toBe('/api/v1/tasks/abc');
    expect(buildUrl('/healthz')).toBe('/healthz');
  });

  it('skips undefined, null and empty query values', () => {
    expect(buildUrl('/projects', { q: '', page: undefined, owner: null, limit: 5 })).toBe('/api/v1/projects?limit=5');
  });

  it('omits the query string when every value is skipped', () => {
    expect(buildUrl('/projects', { q: '' })).toBe('/api/v1/projects');
  });
});

describe('unwrapList', () => {
  it('accepts a bare array or a keyed envelope', () => {
    expect(unwrapList([1, 2], 'items')).toEqual([1, 2]);
    expect(unwrapList({ items: [3] }, 'items')).toEqual([3]);
    expect(unwrapList({}, 'items')).toEqual([]);
  });
});
