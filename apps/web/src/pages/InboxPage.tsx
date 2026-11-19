import { useState } from 'react';
import { Link } from 'react-router';
import { errorMessage } from '../api/client';
import { endpoints } from '../api/endpoints';
import type { Request, RequestStatus, RequestType } from '../api/types';
import { StateBadge } from '../components/StateBadge';
import { Badge, Button, Card, Empty, ErrorMessage, Field, PageHeader, Select, Spinner } from '../components/ui';
import { formatRelative, priorityLabel } from '../lib/format';
import { useAuth } from '../state/AuthContext';
import { useConnection, useStreamEvent } from '../state/ConnectionContext';
import { useAsync } from '../state/useApi';

const STATUSES: (RequestStatus | '')[] = ['', 'new', 'triaged', 'in_progress', 'waiting_input', 'review', 'done', 'rejected', 'failed'];
const TYPES: (RequestType | '')[] = ['', 'bug', 'feature', 'improvement', 'refactor', 'performance', 'security', 'docs', 'release'];

export function InboxPage() {
  const { can } = useAuth();
  const { resyncToken } = useConnection();
  const [status, setStatus] = useState<RequestStatus | ''>('');
  const [type, setType] = useState<RequestType | ''>('');
  const requests = useAsync(
    () => endpoints.listRequests({ status, type, limit: 100 }),
    [status, type, resyncToken],
  );
  const [actionError, setActionError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  useStreamEvent('request_updated', () => requests.reload());

  const act = async (id: string, fn: () => Promise<unknown>) => {
    setBusyId(id);
    setActionError(null);
    try {
      await fn();
      requests.reload();
    } catch (err) {
      setActionError(errorMessage(err));
    } finally {
      setBusyId(null);
    }
  };

  return (
    <>
      <PageHeader title="Inbox" subtitle="Requests from SDKs, the CLI and the dashboard across all projects." />

      <div className="mb-4 grid gap-3 md:grid-cols-3">
        <Field id="inbox-status" label="Status">
          <Select id="inbox-status" value={status} onChange={(e) => setStatus(e.target.value as RequestStatus | '')}>
            {STATUSES.map((s) => (
              <option key={s || 'all'} value={s}>
                {s || 'All statuses'}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="inbox-type" label="Type">
          <Select id="inbox-type" value={type} onChange={(e) => setType(e.target.value as RequestType | '')}>
            {TYPES.map((t) => (
              <option key={t || 'all'} value={t}>
                {t || 'All types'}
              </option>
            ))}
          </Select>
        </Field>
      </div>

      <ErrorMessage error={requests.error ?? actionError} />
      {requests.loading && !requests.data ? <Spinner /> : null}
      {requests.data && requests.data.length === 0 ? <Empty>Nothing in the inbox for these filters.</Empty> : null}

      <Card>
        <ul className="divide-y divide-slate-100 dark:divide-slate-800">
          {requests.data?.map((r: Request) => (
            <li key={r.id} className="flex flex-col gap-2 py-3 md:flex-row md:items-center md:justify-between">
              <div className="min-w-0">
                <p className="font-medium">{r.title}</p>
                <p className="flex flex-wrap items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
                  <Badge>{r.type}</Badge>
                  <span>{priorityLabel(r.priority)}</span>
                  <span>{r.source}</span>
                  <Link to={`/projects/${r.projectId}`} className="text-indigo-700 hover:underline dark:text-indigo-300">
                    Project
                  </Link>
                  <span>{formatRelative(r.createdAt)}</span>
                </p>
                {r.description ? <p className="mt-1 line-clamp-2 text-sm text-slate-600 dark:text-slate-300">{r.description}</p> : null}
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Badge>{r.status}</Badge>
                {r.task ? <StateBadge state={r.task.state} /> : null}
                {r.taskId ? (
                  <Link to={`/tasks/${r.taskId}`} className="text-sm text-indigo-700 hover:underline dark:text-indigo-300">
                    View task
                  </Link>
                ) : null}
                {can('manageRequests') && r.status === 'new' ? (
                  <Button disabled={busyId === r.id} onClick={() => void act(r.id, () => endpoints.updateRequest(r.id, { status: 'triaged' }))}>
                    Triage
                  </Button>
                ) : null}
                {can('manageRequests') && (r.status === 'new' || r.status === 'triaged') && !r.taskId ? (
                  <Button variant="primary" disabled={busyId === r.id} onClick={() => void act(r.id, () => endpoints.startRequestTask(r.id))}>
                    Start task
                  </Button>
                ) : null}
                {can('manageRequests') && (r.status === 'new' || r.status === 'triaged') ? (
                  <Button variant="danger" disabled={busyId === r.id} onClick={() => void act(r.id, () => endpoints.updateRequest(r.id, { status: 'rejected' }))}>
                    Reject
                  </Button>
                ) : null}
              </div>
            </li>
          ))}
        </ul>
      </Card>
    </>
  );
}
