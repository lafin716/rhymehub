import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { endpoints } from '../api/endpoints';
import { createEventStreamClient, type StreamEventType, type StreamStatus } from '../api/sse';

export type StreamListener = (type: StreamEventType, data: unknown, lastEventId: string) => void;

export interface ConnectionValue {
  /** Status of the SSE stream (`/api/v1/events/stream`). */
  stream: StreamStatus;
  /** False while /healthz is unreachable (server down or restarting). */
  serverOnline: boolean;
  /** True after a self-update restarted the server (version changed or draining). */
  updateAvailable: boolean;
  /** Bumps every time the stream reopens after a drop. Pages back-fill when it changes. */
  resyncToken: number;
  subscribe: (listener: StreamListener) => () => void;
  reloadPage: () => void;
}

const noopSubscribe = () => () => undefined;

const DEFAULT_VALUE: ConnectionValue = {
  stream: 'closed',
  serverOnline: true,
  updateAvailable: false,
  resyncToken: 0,
  subscribe: noopSubscribe,
  reloadPage: () => window.location.reload(),
};

const ConnectionContext = createContext<ConnectionValue>(DEFAULT_VALUE);

/** Healthz polling: slow while healthy (detects self-update restarts), fast while down. */
const HEALTH_INTERVAL_OK_MS = 30_000;
const HEALTH_INTERVAL_DOWN_MS = 3_000;

export function ConnectionProvider({ children, enabled = true }: { children: ReactNode; enabled?: boolean }) {
  const listeners = useRef(new Set<StreamListener>());
  const [stream, setStream] = useState<StreamStatus>('connecting');
  const [serverOnline, setServerOnline] = useState(true);
  const [updateAvailable, setUpdateAvailable] = useState(false);
  const [resyncToken, setResyncToken] = useState(0);
  const baseVersion = useRef<string | null>(null);
  const openedBefore = useRef(false);

  const subscribe = useCallback((listener: StreamListener) => {
    listeners.current.add(listener);
    return () => {
      listeners.current.delete(listener);
    };
  }, []);

  useEffect(() => {
    if (!enabled) return;
    const client = createEventStreamClient({
      onEvent: (type, data, lastEventId) => {
        for (const listener of listeners.current) listener(type, data, lastEventId);
      },
      onStatus: (next) => {
        setStream(next);
        if (next === 'open') {
          if (openedBefore.current) setResyncToken((n) => n + 1);
          openedBefore.current = true;
        }
      },
    });
    return () => client.close();
  }, [enabled]);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const check = async () => {
      let delay = HEALTH_INTERVAL_OK_MS;
      try {
        const health = await endpoints.health();
        if (cancelled) return;
        setServerOnline(true);
        if (baseVersion.current === null) {
          baseVersion.current = health.version;
        } else if (health.version !== baseVersion.current || health.draining) {
          setUpdateAvailable(true);
        }
      } catch {
        if (cancelled) return;
        setServerOnline(false);
        delay = HEALTH_INTERVAL_DOWN_MS;
      }
      if (!cancelled) timer = setTimeout(() => void check(), delay);
    };

    void check();
    return () => {
      cancelled = true;
      if (timer !== undefined) clearTimeout(timer);
    };
  }, [enabled]);

  const value = useMemo<ConnectionValue>(
    () => ({
      stream,
      serverOnline,
      updateAvailable,
      resyncToken,
      subscribe,
      reloadPage: () => window.location.reload(),
    }),
    [stream, serverOnline, updateAvailable, resyncToken, subscribe],
  );

  return <ConnectionContext.Provider value={value}>{children}</ConnectionContext.Provider>;
}

export function useConnection(): ConnectionValue {
  return useContext(ConnectionContext);
}

/** Calls `handler` for every stream frame of `type`. The latest handler is always used. */
export function useStreamEvent(type: StreamEventType, handler: (data: unknown, lastEventId: string) => void): void {
  const { subscribe } = useConnection();
  const handlerRef = useRef(handler);
  useEffect(() => {
    handlerRef.current = handler;
  });
  useEffect(
    () =>
      subscribe((eventType, data, lastEventId) => {
        if (eventType === type) handlerRef.current(data, lastEventId);
      }),
    [subscribe, type],
  );
}
