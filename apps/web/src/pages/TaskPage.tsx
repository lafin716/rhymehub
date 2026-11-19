import { useEffect, useRef, useState } from 'react';
import { Link, useParams } from 'react-router';
import { endpoints, fetchEventBacklog } from '../api/endpoints';
import type { StreamTaskEvent, Task, TaskEvent } from '../api/types';

import { DiffViewer } from '../components/DiffViewer';
import { EventTimeline } from '../components/EventTimeline';
import { StateBadge } from '../components/StateBadge';
import { TaskActions } from '../components/TaskActions';
import { TaskComposer } from '../components/TaskComposer';
import { Badge, Card, ErrorMessage, PageHeader, Spinner } from '../components/ui';
import { formatCost, formatRelative } from '../lib/format';
import { lastSeq, mergeEvents } from '../lib/events';
import { useAuth } from '../state/AuthContext';
import { useConnection, useStreamEvent } from '../state/ConnectionContext';
import { useAsync } from '../state/useApi';

type Tab = 'chat' | 'diff';

export function TaskPage() {
  const { id = '' } = useParams();
  const { role } = useAuth();
  const { resyncToken } = useConnection();
  const task = useAsync(() => endpoints.getTask(id), [id]);
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [eventError, setEventError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>('chat');
  const eventsRef = useRef<TaskEvent[]>([]);
  eventsRef.current = events;

  // Backfill from seq 0 on mount (and whenever the task id changes).
  useEffect(() => {
    let active = true;
    setEvents([]);
    fetchEventBacklog(id, 0)
      .then((backlog) => {
        if (active) setEvents((prev) => mergeEvents(prev, backlog));
      })
      .catch((err: unknown) => {
        if (active) setEventError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      active = false;
    };
  }, [id]);

  // The stream reopened: fetch anything missed after the last seq we hold.
  const firstResync = useRef(true);
  useEffect(() => {
    if (firstResync.current) {
      firstResync.current = false;
      return;
    }
    let active = true;
    fetchEventBacklog(id, lastSeq(eventsRef.current))
      .then((missed) => {
        if (active && missed.length) setEvents((prev) => mergeEvents(prev, missed));
      })
      .catch(() => undefined);
    return () => {
      active = false;
    };
  }, [resyncToken, id]);

  useStreamEvent('task_event', (data) => {
    const event = data as StreamTaskEvent;
    if (event.taskId === id) setEvents((prev) => mergeEvents(prev, [event]));
  });

  useStreamEvent('task_updated', (data) => {
    const summary = data as { id?: string } | null;
    if (summary?.id === id) void endpoints.getTask(id).then((fresh) => task.setData(fresh)).catch(() => undefined);
  });

  if (task.loading && !task.data) return <Spinner label="Loading task…" />;
  if (task.error && !task.data) return <ErrorMessage error={task.error} />;
  if (!task.data) return null;

  const current = task.data;

  return (
    <>
      <PageHeader
        title={current.title}
        subtitle={
          <span className="flex flex-wrap items-center gap-2">
            <StateBadge state={current.state} />
            <Badge>{current.agent}{current.model ? ` · ${current.model}` : ''}</Badge>
            {current.branch ? <span className="font-mono text-xs">{current.branch}</span> : null}
            <span>Updated {formatRelative(current.updatedAt)}</span>
            <span>{formatCost(current.costUsd)}</span>
            <Link to={`/projects/${current.projectId}`} className="text-indigo-700 hover:underline dark:text-indigo-300">
              Project
            </Link>
          </span>
        }
      />

      <div className="mb-4">
        <TaskActions task={current} role={role} onChanged={(next: Task) => task.setData(next)} />
      </div>

      {current.failureReason || current.error ? (
        <div className="mb-4">
          <ErrorMessage error={current.failureReason || current.error} />
        </div>
      ) : null}

      <div role="tablist" aria-label="Task views" className="mb-3 flex gap-1 border-b border-slate-200 dark:border-slate-700">
        {(['chat', 'diff'] as const).map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={`-mb-px border-b-2 px-3 py-2 text-sm ${
              tab === t ? 'border-indigo-600 font-medium text-indigo-700 dark:text-indigo-300' : 'border-transparent text-slate-500'
            }`}
          >
            {t === 'chat' ? 'Conversation' : 'Changes'}
          </button>
        ))}
      </div>

      {tab === 'chat' ? (
        <Card>
          <ErrorMessage error={eventError} />
          <div className="max-h-[60vh] overflow-y-auto pr-1">
            <EventTimeline events={events} emptyText="No activity yet." />
          </div>
          <div className="mt-4 border-t border-slate-200 pt-4 dark:border-slate-700">
            <TaskComposer task={current} role={role} onChanged={(next: Task) => task.setData(next)} />
          </div>
        </Card>
      ) : (
        <DiffPanel taskId={id} />
      )}
    </>
  );
}

function DiffPanel({ taskId }: { taskId: string }) {
  const diff = useAsync(() => endpoints.taskDiff(taskId), [taskId]);
  if (diff.loading && !diff.data) return <Spinner label="Loading diff…" />;
  if (diff.error) return <ErrorMessage error={diff.error} />;
  if (!diff.data) return null;
  return (
    <Card>
      <DiffViewer patch={diff.data.patch} stat={diff.data.stat} />
    </Card>
  );
}
