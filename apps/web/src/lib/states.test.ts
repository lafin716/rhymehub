import { describe, expect, it } from 'vitest';
import type { TaskState, TaskSummary } from '../api/types';
import { isRunning, isTerminal, stateLabel, stateTone, summarizeTasks, taskActions } from './states';

function summary(id: string, state: TaskState): TaskSummary {
  return { id, state } as unknown as TaskSummary;
}

describe('taskActions', () => {
  it('shows Approve and Reject only to maintainers at REVIEW_READY', () => {
    expect(taskActions('REVIEW_READY', 'developer').approve).toBe(false);
    expect(taskActions('REVIEW_READY', 'developer').reject).toBe(false);
    expect(taskActions('REVIEW_READY', 'maintainer').approve).toBe(true);
    expect(taskActions('REVIEW_READY', 'maintainer').reject).toBe(true);
    expect(taskActions('RUNNING', 'maintainer').approve).toBe(false);
  });

  it('offers answer only at WAITING_INPUT and permission only at WAITING_APPROVAL', () => {
    expect(taskActions('WAITING_INPUT', 'developer').answer).toBe(true);
    expect(taskActions('WAITING_APPROVAL', 'developer').answer).toBe(false);
    expect(taskActions('WAITING_APPROVAL', 'developer').permission).toBe(true);
    expect(taskActions('WAITING_INPUT', 'developer').permission).toBe(false);
  });

  it('offers retry only at FAILED and hides everything from reporters', () => {
    expect(taskActions('FAILED', 'developer').retry).toBe(true);
    expect(taskActions('RUNNING', 'developer').retry).toBe(false);
    expect(taskActions('FAILED', 'reporter')).toEqual({
      followUp: false,
      answer: false,
      permission: false,
      cancel: false,
      retry: false,
      approve: false,
      reject: false,
    });
  });

  it('does not allow cancel on terminal states', () => {
    expect(taskActions('COMPLETED', 'admin').cancel).toBe(false);
    expect(taskActions('CANCELLED', 'admin').cancel).toBe(false);
    expect(taskActions('RUNNING', 'admin').cancel).toBe(true);
  });
});

describe('state helpers', () => {
  it('classifies running, terminal and attention states', () => {
    expect(isRunning('RUNNING')).toBe(true);
    expect(isRunning('QUEUED')).toBe(false);
    expect(isTerminal('ROLLED_BACK')).toBe(true);
    expect(isTerminal('FAILED')).toBe(false);
  });

  it('labels and tones every state', () => {
    expect(stateLabel('WAITING_INPUT')).toBe('Needs answer');
    expect(stateTone('FAILED')).toBe('danger');
    expect(stateTone('REVIEW_READY')).toBe('success');
  });

  it('summarizes tasks into buckets', () => {
    const result = summarizeTasks([
      summary('a', 'RUNNING'),
      summary('b', 'QUEUED'),
      summary('c', 'WAITING_INPUT'),
      summary('d', 'FAILED'),
      summary('e', 'COMPLETED'),
    ]);
    expect(result.running.map((t) => t.id)).toEqual(['a']);
    expect(result.queued.map((t) => t.id)).toEqual(['b']);
    expect(result.attention.map((t) => t.id)).toEqual(['c']);
    expect(result.failed.map((t) => t.id)).toEqual(['d']);
  });
});
