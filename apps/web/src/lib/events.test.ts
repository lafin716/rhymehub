import { describe, expect, it } from 'vitest';
import type { TaskEvent } from '../api/types';
import { lastSeq, mergeEvents, payloadString, payloadText, prettyJson } from './events';

function ev(seq: number, text = `e${seq}`): TaskEvent {
  return {
    taskId: 't1',
    seq,
    type: 'agent_text',
    payload: { text },
    createdAt: '2026-01-01T00:00:00Z',
  } as unknown as TaskEvent;
}

describe('mergeEvents', () => {
  it('de-duplicates overlapping pages by seq and keeps ascending order', () => {
    const merged = mergeEvents([ev(1), ev(2), ev(3)], [ev(3, 'newer'), ev(4)]);
    expect(merged.map((e) => e.seq)).toEqual([1, 2, 3, 4]);
    expect(merged[2]!.payload).toEqual({ text: 'newer' });
  });

  it('sorts out-of-order incoming frames', () => {
    expect(mergeEvents([ev(5)], [ev(2), ev(4)]).map((e) => e.seq)).toEqual([2, 4, 5]);
  });
});

describe('lastSeq', () => {
  it('returns the highest seq or 0 when empty', () => {
    expect(lastSeq([])).toBe(0);
    expect(lastSeq([ev(2), ev(7), ev(3)])).toBe(7);
  });
});

describe('payload helpers', () => {
  it('reads text and stringifies scalars', () => {
    expect(payloadText({ text: 'hi' })).toBe('hi');
    expect(payloadText({ text: 3 })).toBe('');
    expect(payloadString({ n: 3 }, 'n')).toBe('3');
    expect(payloadString({ b: true }, 'b')).toBe('true');
    expect(payloadString({}, 'missing')).toBe('');
  });

  it('prints JSON and passes strings through', () => {
    expect(prettyJson('raw')).toBe('raw');
    expect(prettyJson({ a: 1 })).toBe('{\n  "a": 1\n}');
  });

  it('never throws on circular values', () => {
    const circular: Record<string, unknown> = {};
    circular.self = circular;
    expect(() => prettyJson(circular)).not.toThrow();
  });
});
