import { describe, expect, it, vi } from "vitest";
import { RhymehubClient, RhymehubError, createMemoryStorage, INSTALLATION_ID_STORAGE_KEY } from "./index";

const KEY = "rhk_test_key_123";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function makeClient(fetchImpl: typeof fetch, storage = createMemoryStorage()) {
  return new RhymehubClient({
    projectId: "droidpad",
    endpoint: "https://core.example.ts.net:7780/",
    projectKey: KEY,
    appVersion: "1.2.3",
    platform: "darwin",
    storage,
    fetch: fetchImpl,
    retry: { attempts: 2, baseDelayMs: 0 },
  });
}

const sampleRequest = {
  id: "req_1",
  projectId: "droidpad",
  type: "bug",
  title: "Crash",
  description: "boom",
  status: "new",
  createdAt: "2026-11-19T00:00:00Z",
  updatedAt: "2026-11-19T00:00:00Z",
  progress: { state: "new", label: "New", percent: 0 },
};

describe("RhymehubClient construction", () => {
  it("rejects rht_ admin tokens", () => {
    expect(() => makeClient(vi.fn() as unknown as typeof fetch, undefined)).not.toThrow();
    expect(
      () =>
        new RhymehubClient({
          projectId: "p",
          endpoint: "",
          projectKey: "rht_admin",
          fetch: vi.fn() as unknown as typeof fetch,
        }),
    ).toThrowError(RhymehubError);
  });

  it("rejects missing keys", () => {
    expect(
      () => new RhymehubClient({ projectId: "p", endpoint: "", projectKey: "", fetch: vi.fn() as unknown as typeof fetch }),
    ).toThrowError(/projectKey is required/);
  });

  it("rejects non-http endpoints", () => {
    expect(
      () =>
        new RhymehubClient({
          projectId: "p",
          endpoint: "ftp://nope",
          projectKey: KEY,
          fetch: vi.fn() as unknown as typeof fetch,
        }),
    ).toThrow(/endpoint must be an http/);
  });
});

describe("installationId", () => {
  it("is generated once and persisted under rhymehub.installationId", () => {
    const storage = createMemoryStorage();
    const first = makeClient(vi.fn() as unknown as typeof fetch, storage).installationId;
    expect(first).toMatch(/^[0-9a-f-]{36}$/);
    expect(storage.getItem(INSTALLATION_ID_STORAGE_KEY)).toBe(first);
    const second = makeClient(vi.fn() as unknown as typeof fetch, storage).installationId;
    expect(second).toBe(first);
  });

  it("replaces a corrupt stored value", () => {
    const storage = createMemoryStorage();
    storage.setItem(INSTALLATION_ID_STORAGE_KEY, "not-a-uuid");
    const id = makeClient(vi.fn() as unknown as typeof fetch, storage).installationId;
    expect(id).not.toBe("not-a-uuid");
    expect(storage.getItem(INSTALLATION_ID_STORAGE_KEY)).toBe(id);
  });
});

describe("requests.create", () => {
  it("posts camelCase body with key header and Idempotency-Key, redacting the description", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(201, sampleRequest));
    const client = makeClient(fetchMock as unknown as typeof fetch);

    const created = await client.requests.create({
      type: "bug",
      title: "Crash",
      description: "login failed for alice@example.com with token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefghijk",
    });
    expect(created.id).toBe("req_1");

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("https://core.example.ts.net:7780/api/v1/sdk/requests");
    expect(init.method).toBe("POST");
    const headers = init.headers as Record<string, string>;
    expect(headers["X-Rhymehub-Key"]).toBe(KEY);
    expect(headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);

    const body = JSON.parse(init.body as string);
    expect(body.type).toBe("bug");
    expect(body.installationId).toBe(client.installationId);
    expect(body.description).not.toContain("alice@example.com");
    expect(body.description).toContain("[REDACTED");
    expect(body.description).not.toContain("eyJhbGciOiJIUzI1NiJ9");
  });

  it("retries a 503 with the same Idempotency-Key", async () => {
    const seen: string[] = [];
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(503, { error: { code: "unavailable", message: "busy" } }))
      .mockImplementationOnce(async (_url: string, init: RequestInit) => {
        seen.push((init.headers as Record<string, string>)["Idempotency-Key"]);
        return jsonResponse(201, sampleRequest);
      });
    const client = makeClient(fetchMock as unknown as typeof fetch);
    await client.requests.create({ type: "bug", description: "x" });
    const first = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1].headers as Record<string, string>;
    expect(seen[0]).toBe(first["Idempotency-Key"]);
  });

  it("rejects empty descriptions and oversized descriptions without calling fetch", async () => {
    const fetchMock = vi.fn();
    const client = makeClient(fetchMock as unknown as typeof fetch);
    await expect(client.requests.create({ type: "bug", description: "   " })).rejects.toThrow(/description is required/);
    await expect(
      client.requests.create({ type: "bug", description: "a".repeat(64 * 1024 + 1) }),
    ).rejects.toThrow(/64 KiB/);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("rejects unsupported attachment MIME types", async () => {
    const client = makeClient(vi.fn() as unknown as typeof fetch);
    await expect(
      client.requests.create({
        type: "bug",
        description: "x",
        attachments: [{ kind: "screenshot", filename: "a.exe", mime: "application/x-msdownload", data: "MZ" }],
      }),
    ).rejects.toThrow(/Unsupported attachment type/);
  });

  it("sends attachments as base64 JSON", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(201, sampleRequest));
    const client = makeClient(fetchMock as unknown as typeof fetch);
    await client.requests.create({
      type: "bug",
      description: "see log",
      attachments: [{ kind: "log", filename: "../my log.txt", mime: "text/plain", data: "hello" }],
    });
    const body = JSON.parse((fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1].body as string);
    expect(body.attachments).toEqual([
      { kind: "log", filename: "my_log.txt", mime: "text/plain", dataBase64: "aGVsbG8=" },
    ]);
  });
});

describe("requests.list / get / answer", () => {
  it("lists with installationId query and unwraps requests", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(200, { requests: [sampleRequest] }));
    const client = makeClient(fetchMock as unknown as typeof fetch);
    const list = await client.requests.list();
    expect(list).toHaveLength(1);
    const url = (fetchMock.mock.calls[0] as unknown as [string])[0];
    expect(url).toBe(
      `https://core.example.ts.net:7780/api/v1/sdk/requests?installationId=${client.installationId}`,
    );
  });

  it("answers with installationId and redacted text", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(200, { ...sampleRequest, status: "in_progress" }));
    const client = makeClient(fetchMock as unknown as typeof fetch);
    await client.requests.answer("req_1", "use key sk-ant-api03-abcdefghijklmnopqrstuvwx");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toMatch(/\/api\/v1\/sdk\/requests\/req_1\/answer$/);
    const body = JSON.parse(init.body as string);
    expect(body.installationId).toBe(client.installationId);
    expect(body.text).not.toContain("sk-ant-api03");
  });

  it("maps API error bodies to RhymehubError", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(404, { error: { code: "not_found", message: "request not found" } }),
    );
    const client = makeClient(fetchMock as unknown as typeof fetch);
    const err = await client.requests.get("missing").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(RhymehubError);
    expect(err).toMatchObject({ status: 404, code: "not_found", message: "request not found" });
    // 404 is not retryable: exactly one call.
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("retries GET on network failure and then succeeds", async () => {
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("fetch failed"))
      .mockResolvedValueOnce(jsonResponse(200, sampleRequest));
    const client = makeClient(fetchMock as unknown as typeof fetch);
    const req = await client.requests.get("req_1");
    expect(req.id).toBe("req_1");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});

describe("requests.watch", () => {
  it("emits changes and stops on a terminal status", async () => {
    vi.useFakeTimers();
    try {
      const statuses = ["new", "in_progress", "done"];
      const fetchMock = vi.fn(async () => {
        const status = statuses.shift() ?? "done";
        return jsonResponse(200, { ...sampleRequest, status });
      });
      const client = makeClient(fetchMock as unknown as typeof fetch);
      const updates: string[] = [];
      client.requests.watch("req_1", (r) => updates.push(r.status), { intervalMs: 1000 });

      for (let i = 0; i < 5; i++) {
        await vi.advanceTimersByTimeAsync(1000);
      }
      expect(updates).toEqual(["new", "in_progress", "done"]);
      expect(fetchMock).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops polling when unsubscribed", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn(async () => jsonResponse(200, sampleRequest));
      const client = makeClient(fetchMock as unknown as typeof fetch);
      const stop = client.requests.watch("req_1", () => {}, { intervalMs: 1000 });
      await vi.advanceTimersByTimeAsync(0);
      stop();
      await vi.advanceTimersByTimeAsync(10_000);
      expect(fetchMock).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("releases.checkForUpdate", () => {
  it("passes currentVersion and platform and returns the API shape", async () => {
    const payload = {
      latestVersion: "1.3.0",
      updateAvailable: true,
      notes: "fixes",
      publishedAt: "2026-11-18T00:00:00Z",
      update: { kind: "download", url: "https://example.com/app.dmg" },
    };
    const fetchMock = vi.fn(async () => jsonResponse(200, payload));
    const client = makeClient(fetchMock as unknown as typeof fetch);
    const result = await client.releases.checkForUpdate();
    expect(result).toEqual(payload);
    expect((fetchMock.mock.calls[0] as unknown as [string])[0]).toBe(
      "https://core.example.ts.net:7780/api/v1/sdk/releases/latest?currentVersion=1.2.3&platform=darwin",
    );
  });
});

describe("diagnostics", () => {
  it("captures console calls only after opt-in and restores the original on stop", () => {
    const client = makeClient(vi.fn() as unknown as typeof fetch);
    const original = console.warn;
    const stop = client.diagnostics.captureConsole({ levels: ["warn"] });
    console.warn("disk almost full");
    const snap = client.diagnostics.snapshot();
    expect(snap.some((d) => d.kind === "log" && d.content.includes("disk almost full"))).toBe(true);
    stop();
    expect(console.warn).toBe(original);
    client.diagnostics.dispose();
  });
});
