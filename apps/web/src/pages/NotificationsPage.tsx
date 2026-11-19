import { useState } from 'react';
import { useNavigate } from 'react-router';
import { errorMessage } from '../api/client';
import { endpoints } from '../api/endpoints';
import type { AppNotification } from '../api/types';
import { Button, Card, Empty, ErrorMessage, PageHeader, Spinner } from '../components/ui';
import { formatRelative } from '../lib/format';
import { useConnection, useStreamEvent } from '../state/ConnectionContext';
import { useAsync } from '../state/useApi';

/** Only same-origin app paths are followed; anything else is shown as text. */
export function isSafeInternalPath(url: string): boolean {
  return url.startsWith('/') && !url.startsWith('//') && !url.startsWith('/\\');
}

export function NotificationsPage() {
  const navigate = useNavigate();
  const { resyncToken } = useConnection();
  const list = useAsync(() => endpoints.listNotifications(100), [resyncToken]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useStreamEvent('notification', () => list.reload());

  const unread = (list.data ?? []).filter((n) => !n.readAt);

  const markRead = async (ids: string[]) => {
    if (ids.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      await endpoints.markNotificationsRead(ids);
      list.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const open = async (n: AppNotification) => {
    if (!n.readAt) await markRead([n.id]);
    if (n.url && isSafeInternalPath(n.url)) navigate(n.url);
  };

  return (
    <>
      <PageHeader
        title="Notifications"
        subtitle={unread.length ? `${unread.length} unread` : 'All caught up.'}
        actions={
          unread.length ? (
            <Button disabled={busy} onClick={() => void markRead(unread.map((n) => n.id))}>
              Mark all read
            </Button>
          ) : null
        }
      />
      <ErrorMessage error={list.error ?? error} />
      {list.loading && !list.data ? <Spinner /> : null}
      {list.data && list.data.length === 0 ? <Empty>No notifications yet.</Empty> : null}
      <Card>
        <ul className="divide-y divide-slate-100 dark:divide-slate-800">
          {list.data?.map((n) => (
            <li key={n.id} className="flex flex-col gap-1 py-3 md:flex-row md:items-start md:justify-between">
              <div className="min-w-0">
                <p className={`text-sm ${n.readAt ? 'text-slate-600 dark:text-slate-300' : 'font-semibold'}`}>
                  {!n.readAt ? <span aria-label="unread" className="mr-1 inline-block h-2 w-2 rounded-full bg-indigo-500" /> : null}
                  {n.title}
                </p>
                {n.body ? <p className="text-sm text-slate-600 dark:text-slate-300">{n.body}</p> : null}
                <p className="text-xs text-slate-400">{formatRelative(n.createdAt)}</p>
              </div>
              <div className="flex gap-2">
                {n.url && isSafeInternalPath(n.url) ? (
                  <Button onClick={() => void open(n)}>Open</Button>
                ) : null}
                {!n.readAt ? (
                  <Button variant="ghost" disabled={busy} onClick={() => void markRead([n.id])}>
                    Mark read
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
