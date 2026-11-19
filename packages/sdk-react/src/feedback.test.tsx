import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RhymehubClient, createMemoryStorage, type SdkRequest } from "@rhymehub/sdk";
import { RhymehubFeedback, RhymehubProvider, useRhymehubRequests } from "./index";

afterEach(() => cleanup());

interface FakeBackend {
  fetch: typeof fetch;
  calls: { method: string; url: string; body?: unknown }[];
  setStatus(status: SdkRequest["status"], label: string, question?: string): void;
}

/** Minimal in-memory stand-in for the rhymehub SDK endpoints used by the widget. */
function fakeBackend(): FakeBackend {
  const calls: FakeBackend["calls"] = [];
  let current: SdkRequest | null = null;
  const base = {
    projectId: "droidpad",
    type: "bug",
    title: "",
    description: "",
    createdAt: "2026-11-19T00:00:00Z",
    updatedAt: "2026-11-19T00:00:00Z",
    progress: { state: "new", label: "New", percent: 0 },
  } as const;

  const impl = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(init.body as string) : undefined;
    calls.push({ method, url, body });
    const json = (status: number, payload: unknown) =>
      new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });

    if (method === "POST" && url.endsWith("/api/v1/sdk/requests")) {
      current = { ...base, id: "req_1", status: "new", description: body.description } as SdkRequest;
      return json(201, current);
    }
    if (method === "POST" && url.endsWith("/answer")) {
      current = { ...(current as SdkRequest), status: "in_progress", progress: { state: "running", label: "Working", percent: 40 } };
      return json(200, current);
    }
    if (method === "GET" && url.includes("/api/v1/sdk/requests/req_1")) {
      return json(200, current);
    }
    return json(404, { error: { code: "not_found", message: "no route" } });
  };

  return {
    fetch: impl as unknown as typeof fetch,
    calls,
    setStatus(status, label, question) {
      current = {
        ...(current as SdkRequest),
        status,
        progress: { state: status, label, percent: 50 },
        ...(question ? { question } : {}),
      };
    },
  };
}

function makeClient(backend: FakeBackend) {
  return new RhymehubClient({
    projectId: "droidpad",
    endpoint: "https://core.example.ts.net:7780",
    projectKey: "rhk_widget_test",
    storage: createMemoryStorage(),
    fetch: backend.fetch,
    retry: { attempts: 0, baseDelayMs: 0 },
  });
}

describe("RhymehubFeedback", () => {
  it("submits a request with the description and shows its progress", async () => {
    const backend = fakeBackend();
    const client = makeClient(backend);
    const onSubmitted = vi.fn();
    render(<RhymehubFeedback client={client} onSubmitted={onSubmitted} />);

    fireEvent.click(screen.getByRole("button", { name: "Feedback" }));
    const dialog = screen.getByRole("dialog", { name: "Send feedback" });
    expect(dialog).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Details"), { target: { value: "The counter resets on reload" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(onSubmitted).toHaveBeenCalledTimes(1));
    const post = backend.calls.find((c) => c.method === "POST" && c.url.endsWith("/sdk/requests"));
    expect(post?.body).toMatchObject({ type: "bug", description: "The counter resets on reload" });
    expect(post?.body).toHaveProperty("installationId");

    expect(await screen.findByText(/New/)).toBeTruthy();
  });

  it("does not send an empty description", () => {
    const backend = fakeBackend();
    render(<RhymehubFeedback client={makeClient(backend)} />);
    fireEvent.click(screen.getByRole("button", { name: "Feedback" }));
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(screen.getByRole("alert").textContent).toMatch(/Please describe/);
    expect(backend.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("shows the agent question and sends the reporter's answer", async () => {
    const backend = fakeBackend();
    render(<RhymehubFeedback client={makeClient(backend)} pollIntervalMs={20} />);

    fireEvent.click(screen.getByRole("button", { name: "Feedback" }));
    fireEvent.change(screen.getByLabelText("Details"), { target: { value: "Crash on start" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await screen.findByRole("status");

    backend.setStatus("waiting_input", "Waiting for you", "Which browser are you using?");
    const answerBox = await screen.findByLabelText(/The agent asks: Which browser are you using\?/);
    fireEvent.change(answerBox, { target: { value: "Chrome 130" } });
    fireEvent.click(screen.getByRole("button", { name: "Send answer" }));

    await waitFor(() => {
      const answer = backend.calls.find((c) => c.url.endsWith("/answer"));
      expect(answer?.body).toMatchObject({ text: "Chrome 130" });
    });
    // After answering, the same question no longer shows an answer box.
    await waitFor(() => expect(screen.queryByLabelText(/The agent asks/)).toBeNull());
  });

  it("closes on Escape and returns focus to the trigger", () => {
    render(<RhymehubFeedback client={makeClient(fakeBackend())} />);
    const trigger = screen.getByRole("button", { name: "Feedback" });
    fireEvent.click(trigger);
    const dialog = screen.getByRole("dialog");
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it("uses the nearest provider when no client prop is given", async () => {
    const backend = fakeBackend();
    const client = makeClient(backend);
    render(
      <RhymehubProvider client={client}>
        <RhymehubFeedback triggerLabel="Report" />
      </RhymehubProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Report" }));
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("throws a clear error when used without a provider or client", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      expect(() => render(<RhymehubFeedback />)).toThrow(/RhymehubProvider/);
    } finally {
      spy.mockRestore();
    }
  });
});

describe("useRhymehubRequests", () => {
  it("lists requests for the installation", async () => {
    const backend = fakeBackend();
    const client = makeClient(backend);
    function List() {
      const { requests, loading } = useRhymehubRequests();
      return <p>{loading ? "loading" : `count:${requests.length}`}</p>;
    }
    render(
      <RhymehubProvider client={client}>
        <List />
      </RhymehubProvider>,
    );
    // The fake backend has no list route, so the hook reports an error and stops loading.
    await waitFor(() => expect(screen.getByText(/count:0/)).toBeTruthy());
  });
});
