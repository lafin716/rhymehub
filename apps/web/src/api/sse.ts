// Server-Sent Events client for GET /api/v1/events/stream.
//
// The browser's EventSource keeps the Last-Event-ID header across its own automatic retries.
// When the browser gives up (readyState CLOSED, e.g. a non-200 response), this client opens a
// fresh EventSource after an exponential backoff. A fresh connection cannot send Last-Event-ID,
// so callers must back-fill from GET /tasks/{id}/events?after=<seq> on every reopen (the
// `resync` signal from ConnectionProvider).

export const STREAM_URL = '/api/v1/events/stream';

export const STREAM_EVENT_TYPES = ['task_event', 'task_updated', 'request_updated', 'notification', 'system'] as const;
export type StreamEventType = (typeof STREAM_EVENT_TYPES)[number];

export type StreamStatus = 'connecting' | 'open' | 'reconnecting' | 'closed';

/** The subset of EventSource this client uses; lets tests supply a fake. */
export interface EventSourceLike {
  readonly readyState: number;
  addEventListener(type: string, listener: (ev: Event) => void): void;
  close(): void;
}

export type EventSourceFactory = (url: string) => EventSourceLike;

export interface EventStreamOptions {
  url?: string;
  onEvent: (type: StreamEventType, data: unknown, lastEventId: string) => void;
  onStatus?: (status: StreamStatus) => void;
  createSource?: EventSourceFactory;
  /** Delay before reconnect attempt n (0-based). Defaults to 1s doubling up to 15s. */
  backoffMs?: (attempt: number) => number;
}

export interface EventStreamClient {
  close(): void;
  getStatus(): StreamStatus;
}

const READY_STATE_CLOSED = 2;

export function defaultBackoff(attempt: number): number {
  return Math.min(1000 * 2 ** attempt, 15000);
}

const defaultFactory: EventSourceFactory = (url) => new EventSource(url) as unknown as EventSourceLike;

export function createEventStreamClient(options: EventStreamOptions): EventStreamClient {
  const url = options.url ?? STREAM_URL;
  const createSource = options.createSource ?? defaultFactory;
  const backoff = options.backoffMs ?? defaultBackoff;

  let source: EventSourceLike | null = null;
  let status: StreamStatus = 'connecting';
  let attempt = 0;
  let closed = false;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const setStatus = (next: StreamStatus) => {
    if (next === status) return;
    status = next;
    options.onStatus?.(next);
  };

  const scheduleReconnect = () => {
    setStatus('reconnecting');
    const delay = backoff(attempt);
    attempt += 1;
    timer = setTimeout(() => {
      timer = null;
      connect();
    }, delay);
  };

  const connect = () => {
    if (closed) return;
    const es = createSource(url);
    source = es;

    es.addEventListener('open', () => {
      attempt = 0;
      setStatus('open');
    });

    for (const type of STREAM_EVENT_TYPES) {
      es.addEventListener(type, (ev) => {
        const message = ev as MessageEvent<string>;
        let data: unknown;
        try {
          data = JSON.parse(message.data);
        } catch {
          return; // malformed frame: ignore rather than break the stream
        }
        options.onEvent(type, data, message.lastEventId ?? '');
      });
    }

    es.addEventListener('error', () => {
      if (closed || source !== es) return;
      if (es.readyState === READY_STATE_CLOSED) {
        es.close();
        source = null;
        scheduleReconnect();
      } else {
        // CONNECTING: the browser is retrying on its own and will send Last-Event-ID.
        setStatus('reconnecting');
      }
    });
  };

  connect();

  return {
    close() {
      if (closed) return;
      closed = true;
      if (timer !== null) clearTimeout(timer);
      timer = null;
      source?.close();
      source = null;
      setStatus('closed');
    },
    getStatus: () => status,
  };
}
