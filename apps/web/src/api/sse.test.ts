import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createEventStreamClient, defaultBackoff, type EventSourceLike, type StreamStatus } from './sse';

const READY_CONNECTING = 0;
const READY_CLOSED = 2;

class FakeSource implements EventSourceLike {
  readyState = READY_CONNECTING;
  closed = false;
  private listeners = new Map<string, Array<(ev: Event) => void>>();

  addEventListener(type: string, listener: (ev: Event) => void): void {
    const list = this.listeners.get(type) ?? [];
    list.push(listener);
    this.listeners.set(type, list);
  }

  close(): void {
    this.closed = true;
    this.readyState = READY_CLOSED;
  }

  emit(type: string, ev: unknown = {}): void {
    for (const listener of this.listeners.get(type) ?? []) listener(ev as Event);
  }
}

describe('createEventStreamClient', () => {
  let sources: FakeSource[];
  let statuses: StreamStatus[];
  const createSource = () => {
    const source = new FakeSource();
    sources.push(source);
    return source;
  };

  beforeEach(() => {
    vi.useFakeTimers();
    sources = [];
    statuses = [];
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('marks the stream open and parses typed frames', () => {
    const onEvent = vi.fn();
    createEventStreamClient({
      createSource,
      onEvent,
      onStatus: (s) => statuses.push(s),
      backoffMs: () => 100,
    });

    sources[0]!.emit('open');
    sources[0]!.emit('task_event', { data: JSON.stringify({ taskId: 't1', seq: 3 }), lastEventId: '3' });
    sources[0]!.emit('task_event', { data: 'not json', lastEventId: '4' });

    expect(statuses).toEqual(['open']);
    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledWith('task_event', { taskId: 't1', seq: 3 }, '3');
  });

  it('reconnects with backoff after the browser gives up (CLOSED)', () => {
    const client = createEventStreamClient({
      createSource,
      onEvent: vi.fn(),
      onStatus: (s) => statuses.push(s),
      backoffMs: () => 500,
    });

    sources[0]!.emit('open');
    sources[0]!.readyState = READY_CLOSED;
    sources[0]!.emit('error');

    expect(client.getStatus()).toBe('reconnecting');
    expect(sources).toHaveLength(1);

    vi.advanceTimersByTime(499);
    expect(sources).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(sources).toHaveLength(2);

    sources[1]!.emit('open');
    expect(client.getStatus()).toBe('open');
    client.close();
  });

  it('uses the attempt number to grow the delay', () => {
    const delays: number[] = [];
    createEventStreamClient({
      createSource,
      onEvent: vi.fn(),
      backoffMs: (attempt) => {
        delays.push(attempt);
        return 10;
      },
    });

    for (let i = 0; i < 2; i += 1) {
      sources[i]!.readyState = READY_CLOSED;
      sources[i]!.emit('error');
      vi.advanceTimersByTime(10);
    }

    expect(delays).toEqual([0, 1]);
    expect(defaultBackoff(0)).toBe(1000);
    expect(defaultBackoff(1)).toBe(2000);
    expect(defaultBackoff(10)).toBe(15000);
  });

  it('reports reconnecting without opening a new source while the browser retries', () => {
    const client = createEventStreamClient({
      createSource,
      onEvent: vi.fn(),
      backoffMs: () => 100,
    });

    sources[0]!.readyState = READY_CONNECTING;
    sources[0]!.emit('error');

    expect(client.getStatus()).toBe('reconnecting');
    vi.advanceTimersByTime(1000);
    expect(sources).toHaveLength(1);
    client.close();
  });

  it('stops reconnecting once closed', () => {
    const client = createEventStreamClient({
      createSource,
      onEvent: vi.fn(),
      backoffMs: () => 100,
    });

    sources[0]!.readyState = READY_CLOSED;
    sources[0]!.emit('error');
    client.close();
    vi.advanceTimersByTime(10_000);

    expect(sources).toHaveLength(1);
    expect(client.getStatus()).toBe('closed');
    expect(sources[0]!.closed).toBe(true);
  });
});
