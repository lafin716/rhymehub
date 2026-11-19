import { useCallback, useEffect, useState } from "react";
import type { CheckForUpdateOptions, RhymehubClient, ReleaseCheckResult, SdkRequest } from "@rhymehub/sdk";
import { useClient } from "./provider";

export interface ClientOption {
  /** Explicit client. Defaults to the nearest <RhymehubProvider>. */
  client?: RhymehubClient;
}

export interface RequestsOptions extends ClientOption {
  /** Re-fetch the list every N ms. 0 (default) fetches once on mount and on `refresh()`. */
  intervalMs?: number;
}

export function useRhymehubRequests(options: RequestsOptions = {}): {
  requests: SdkRequest[];
  loading: boolean;
  error: unknown;
  refresh: () => void;
} {
  const client = useClient(options.client);
  const intervalMs = options.intervalMs ?? 0;
  const [requests, setRequests] = useState<SdkRequest[]>([]);
  const [error, setError] = useState<unknown>(undefined);
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      client.requests.list().then(
        (list) => {
          if (cancelled) return;
          setRequests(list);
          setError(undefined);
          setLoading(false);
        },
        (err: unknown) => {
          if (cancelled) return;
          setError(err);
          setLoading(false);
        },
      );
    };
    load();
    const timer = intervalMs > 0 ? setInterval(load, intervalMs) : undefined;
    return () => {
      cancelled = true;
      if (timer !== undefined) clearInterval(timer);
    };
  }, [client, intervalMs, tick]);

  const refresh = useCallback(() => setTick((t) => t + 1), []);
  return { requests, loading, error, refresh };
}

export interface RequestOptions extends ClientOption {
  /** Poll interval. Default 3000 ms. Polling stops on done/rejected/failed. */
  intervalMs?: number;
}

/** Live view of one request. Pass `null` to idle. */
export function useRhymehubRequest(
  id: string | null,
  options: RequestOptions = {},
): { request: SdkRequest | undefined; error: unknown } {
  const client = useClient(options.client);
  const intervalMs = options.intervalMs ?? 3000;
  const [request, setRequest] = useState<SdkRequest | undefined>(undefined);
  const [error, setError] = useState<unknown>(undefined);

  useEffect(() => {
    setRequest(undefined);
    setError(undefined);
    if (id === null) return;
    return client.requests.watch(
      id,
      (next) => {
        setError(undefined);
        setRequest(next);
      },
      { intervalMs, onError: (err) => setError(err) },
    );
  }, [client, id, intervalMs]);

  return { request, error };
}

export interface UpdateOptions extends ClientOption, CheckForUpdateOptions {
  /** Check once on mount. Default true. */
  auto?: boolean;
}

export function useRhymehubUpdate(options: UpdateOptions = {}): {
  result: ReleaseCheckResult | undefined;
  loading: boolean;
  error: unknown;
  check: () => Promise<ReleaseCheckResult | undefined>;
} {
  const client = useClient(options.client);
  const { currentVersion, platform, auto = true } = options;
  const [result, setResult] = useState<ReleaseCheckResult | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(undefined);

  const check = useCallback(async (): Promise<ReleaseCheckResult | undefined> => {
    setLoading(true);
    try {
      const next = await client.releases.checkForUpdate({ currentVersion, platform });
      setResult(next);
      setError(undefined);
      return next;
    } catch (err) {
      setError(err);
      return undefined;
    } finally {
      setLoading(false);
    }
  }, [client, currentVersion, platform]);

  useEffect(() => {
    if (auto) void check();
  }, [auto, check]);

  return { result, loading, error, check };
}
