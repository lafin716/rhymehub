import type { TaskEvent } from '../api/types';

/**
 * Merges events into an ascending, de-duplicated (by seq) list. Used for backlog pages,
 * live SSE frames and reconnect back-fill, which may overlap.
 */
export function mergeEvents(existing: readonly TaskEvent[], incoming: readonly TaskEvent[]): TaskEvent[] {
  const bySeq = new Map<number, TaskEvent>();
  for (const ev of existing) bySeq.set(ev.seq, ev);
  for (const ev of incoming) bySeq.set(ev.seq, ev);
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq);
}

export function lastSeq(events: readonly TaskEvent[]): number {
  let max = 0;
  for (const ev of events) if (ev.seq > max) max = ev.seq;
  return max;
}

export function payloadText(payload: Record<string, unknown>, key = 'text'): string {
  const value = payload[key];
  return typeof value === 'string' ? value : '';
}

export function payloadString(payload: Record<string, unknown>, key: string): string {
  const value = payload[key];
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return '';
}

/** Pretty JSON for tool inputs and outputs; never throws. */
export function prettyJson(value: unknown): string {
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2) ?? '';
  } catch {
    return String(value);
  }
}
