import { useMemo } from 'react';
import { Link } from 'react-router';
import { endpoints } from '../api/endpoints';
import { StateBadge } from '../components/StateBadge';
import { Card, Empty, ErrorMessage, PageHeader, Spinner } from '../components/ui';
import { formatRelative } from '../lib/format';
import { summarizeTasks } from '../lib/states';
import { useAuth } from '../state/AuthContext';
import { useConnection, useStreamEvent } from '../state/ConnectionContext';
import { useAsync } from '../state/useApi';

export function DashboardPage() {
  const { user } = useAuth();
  const { resyncToken } = useConnection();
  const tasks = useAsync(() => endpoints.listTasks({ active: true, limit: 100 }), [resyncToken]);
  const projects = useAsync(() => endpoints.listProjects(), [resyncToken]);

  useStreamEvent('task_updated', () => tasks.reload());

  const summary = useMemo(() => (tasks.data ? summarizeTasks(tasks.data) : null), [tasks.data]);

  return (
    <>
      <PageHeader title="Dashboard" subtitle={user ? `Signed in as ${user.username}` : undefined} />
      <ErrorMessage error={tasks.error} />

      <div className="mb-6 grid grid-cols-2 gap-3 md:grid-cols-4">
        <Tile label="Running" value={summary?.running.length} />
        <Tile label="Queued" value={summary?.queued.length} />
        <Tile label="Needs you" value={summary?.attention.length} highlight />
        <Tile label="Failed" value={summary?.failed.length} />
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <Card title="Needs attention">
          {tasks.loading && !tasks.data ? <Spinner /> : null}
          {summary && summary.attention.length === 0 ? <Empty>Nothing is waiting on you.</Empty> : null}
          <ul className="divide-y divide-slate-100 dark:divide-slate-800">
            {summary?.attention.map((task) => (
              <li key={task.id} className="flex items-center justify-between gap-2 py-2">
                <Link to={`/tasks/${task.id}`} className="truncate text-sm text-indigo-700 hover:underline dark:text-indigo-300">
                  {task.title}
                </Link>
                <StateBadge state={task.state} />
              </li>
            ))}
          </ul>
        </Card>

        <Card title="Projects">
          {projects.loading && !projects.data ? <Spinner /> : null}
          <ErrorMessage error={projects.error} />
          {projects.data && projects.data.length === 0 ? <Empty>No projects yet. Register one to get started.</Empty> : null}
          <ul className="divide-y divide-slate-100 dark:divide-slate-800">
            {projects.data?.map((project) => (
              <li key={project.id} className="flex items-center justify-between gap-2 py-2">
                <Link to={`/projects/${project.id}`} className="truncate text-sm font-medium text-indigo-700 hover:underline dark:text-indigo-300">
                  {project.name}
                </Link>
                <span className="shrink-0 text-xs text-slate-500 dark:text-slate-400">
                  {project.stats.openRequests} open · {project.stats.activeTasks} active · {formatRelative(project.updatedAt)}
                </span>
              </li>
            ))}
          </ul>
        </Card>
      </div>
    </>
  );
}

function Tile({ label, value, highlight = false }: { label: string; value: number | undefined; highlight?: boolean }) {
  return (
    <div
      className={`rounded-lg border p-3 ${
        highlight
          ? 'border-violet-300 bg-violet-50 dark:border-violet-700 dark:bg-violet-950'
          : 'border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-900'
      }`}
    >
      <p className="text-xs uppercase tracking-wide text-slate-500 dark:text-slate-400">{label}</p>
      <p className="mt-1 text-2xl font-semibold">{value ?? '–'}</p>
    </div>
  );
}
