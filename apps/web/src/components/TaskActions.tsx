import { useState } from 'react';
import { endpoints } from '../api/endpoints';
import type { Role, Task } from '../api/types';
import { taskActions } from '../lib/states';
import { Button, ErrorMessage, Textarea } from './ui';

interface TaskActionsProps {
  task: Task;
  role: Role | null;
  onChanged: (task: Task) => void;
}

/**
 * Lifecycle controls for a task: permission decisions, cancel, retry, and (maintainers)
 * approve/reject at REVIEW_READY. Visibility follows lib/states.ts; the server still enforces.
 */
export function TaskActions({ task, role, onChanged }: TaskActionsProps) {
  const actions = taskActions(task.state, role);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState('');
  const [release, setRelease] = useState(false);

  const run = async (fn: () => Promise<Task>) => {
    setBusy(true);
    setError(null);
    try {
      onChanged(await fn());
      setRejecting(false);
      setReason('');
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const hasAnything = actions.permission || actions.cancel || actions.retry || actions.approve || actions.reject;
  if (!hasAnything) return null;

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        {actions.permission && task.pendingApproval?.length ? (
          <div className="flex w-full flex-col gap-2 rounded-md border border-violet-300 bg-violet-50 p-3 dark:border-violet-700 dark:bg-violet-950">
            <p className="text-sm text-violet-900 dark:text-violet-100">
              The agent wants to use {task.pendingApproval.map((d) => d.tool).join(', ')}.
            </p>
            <div className="flex gap-2">
              <Button variant="primary" disabled={busy} onClick={() => void run(() => endpoints.decidePermission(task.id, true))}>
                Allow
              </Button>
              <Button variant="danger" disabled={busy} onClick={() => void run(() => endpoints.decidePermission(task.id, false))}>
                Deny
              </Button>
            </div>
          </div>
        ) : null}

        {actions.approve ? (
          <>
            <Button variant="primary" disabled={busy} onClick={() => void run(() => endpoints.approveTask(task.id, release))}>
              Approve
            </Button>
            <label className="flex items-center gap-1 text-sm text-slate-600 dark:text-slate-300">
              <input type="checkbox" checked={release} onChange={(e) => setRelease(e.target.checked)} />
              Also release
            </label>
          </>
        ) : null}
        {actions.reject ? (
          <Button variant="danger" disabled={busy} onClick={() => setRejecting((v) => !v)} aria-expanded={rejecting}>
            Reject
          </Button>
        ) : null}
        {actions.retry ? (
          <Button disabled={busy} onClick={() => void run(() => endpoints.retryTask(task.id))}>
            Retry
          </Button>
        ) : null}
        {actions.cancel ? (
          <Button variant="ghost" disabled={busy} onClick={() => void run(() => endpoints.cancelTask(task.id))}>
            Cancel task
          </Button>
        ) : null}
      </div>

      {actions.reject && rejecting ? (
        <div className="flex flex-col gap-2">
          <label htmlFor="reject-reason" className="text-sm font-medium text-slate-700 dark:text-slate-200">
            Reason for rejecting
          </label>
          <Textarea id="reject-reason" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} />
          <div>
            <Button variant="danger" disabled={busy || !reason.trim()} onClick={() => void run(() => endpoints.rejectTask(task.id, reason.trim()))}>
              Confirm reject
            </Button>
          </div>
        </div>
      ) : null}

      <ErrorMessage error={error} />
    </div>
  );
}
