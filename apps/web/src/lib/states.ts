import type { Role, TaskState, TaskSummary } from '../api/types';
import { can } from './roles';

export type Tone = 'neutral' | 'info' | 'success' | 'warning' | 'danger' | 'accent';

const TONE: Record<TaskState, Tone> = {
  CREATED: 'neutral',
  QUEUED: 'neutral',
  PREPARING: 'info',
  RUNNING: 'info',
  VALIDATING: 'info',
  RETRYING: 'warning',
  WAITING_INPUT: 'accent',
  WAITING_APPROVAL: 'accent',
  REVIEW_READY: 'success',
  APPROVED: 'success',
  MERGING: 'info',
  RELEASING: 'info',
  COMPLETED: 'success',
  FAILED: 'danger',
  CANCELLED: 'neutral',
  ROLLED_BACK: 'warning',
};

const LABEL: Record<TaskState, string> = {
  CREATED: 'Created',
  QUEUED: 'Queued',
  PREPARING: 'Preparing',
  RUNNING: 'Running',
  WAITING_INPUT: 'Needs answer',
  WAITING_APPROVAL: 'Needs permission',
  VALIDATING: 'Validating',
  REVIEW_READY: 'Ready for review',
  APPROVED: 'Approved',
  MERGING: 'Merging',
  RELEASING: 'Releasing',
  COMPLETED: 'Completed',
  FAILED: 'Failed',
  CANCELLED: 'Cancelled',
  RETRYING: 'Retrying',
  ROLLED_BACK: 'Rolled back',
};

export const RUNNING_STATES: readonly TaskState[] = [
  'PREPARING',
  'RUNNING',
  'VALIDATING',
  'RETRYING',
  'APPROVED',
  'MERGING',
  'RELEASING',
];
export const QUEUED_STATES: readonly TaskState[] = ['CREATED', 'QUEUED'];
export const ATTENTION_STATES: readonly TaskState[] = ['WAITING_INPUT', 'WAITING_APPROVAL', 'REVIEW_READY'];
export const TERMINAL_STATES: readonly TaskState[] = ['COMPLETED', 'CANCELLED', 'ROLLED_BACK'];

const CANCELLABLE: readonly TaskState[] = [
  'CREATED',
  'QUEUED',
  'PREPARING',
  'RUNNING',
  'WAITING_INPUT',
  'WAITING_APPROVAL',
  'VALIDATING',
  'REVIEW_READY',
  'RETRYING',
  'FAILED',
];

export function stateTone(state: TaskState): Tone {
  return TONE[state] ?? 'neutral';
}

export function stateLabel(state: TaskState): string {
  return LABEL[state] ?? state;
}

export function isTerminal(state: TaskState): boolean {
  return TERMINAL_STATES.includes(state);
}

export function isRunning(state: TaskState): boolean {
  return RUNNING_STATES.includes(state);
}

export interface TaskActionAvailability {
  followUp: boolean;
  answer: boolean;
  permission: boolean;
  cancel: boolean;
  retry: boolean;
  approve: boolean;
  reject: boolean;
}

/**
 * Which task actions to show for a role and state. Mirrors the state machine in
 * docs/ARCHITECTURE.md §4; the server remains the authority.
 */
export function taskActions(state: TaskState, role: Role | null | undefined): TaskActionAvailability {
  const dev = can(role, 'createTask');
  const maint = can(role, 'approve');
  return {
    followUp: dev && (state === 'REVIEW_READY' || state === 'FAILED' || state === 'WAITING_INPUT'),
    answer: dev && state === 'WAITING_INPUT',
    permission: dev && state === 'WAITING_APPROVAL',
    cancel: dev && CANCELLABLE.includes(state),
    retry: dev && state === 'FAILED',
    approve: maint && state === 'REVIEW_READY',
    reject: maint && state === 'REVIEW_READY',
  };
}

export function summarizeTasks(tasks: readonly TaskSummary[]) {
  return {
    running: tasks.filter((t) => isRunning(t.state)),
    queued: tasks.filter((t) => QUEUED_STATES.includes(t.state)),
    attention: tasks.filter((t) => ATTENTION_STATES.includes(t.state)),
    failed: tasks.filter((t) => t.state === 'FAILED'),
  };
}
