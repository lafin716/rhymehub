import { useState, type FormEvent } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { errorMessage } from '../api/client';
import { endpoints } from '../api/endpoints';
import type { Project, ProjectKey, ReleaseStrategy, Request, Release, TaskSummary } from '../api/types';
import { SdkSetup } from '../components/SdkSetup';
import { StateBadge } from '../components/StateBadge';
import { Badge, Button, Card, Empty, ErrorMessage, Field, Input, PageHeader, Select, Spinner, Textarea } from '../components/ui';
import { formatRelative, priorityLabel } from '../lib/format';
import { useAuth } from '../state/AuthContext';
import { useConnection, useStreamEvent } from '../state/ConnectionContext';
import { useAsync } from '../state/useApi';

type Tab = 'overview' | 'tasks' | 'requests' | 'releases' | 'keys' | 'sdk' | 'settings';

const TABS: { id: Tab; label: string }[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'tasks', label: 'Tasks' },
  { id: 'requests', label: 'Requests' },
  { id: 'releases', label: 'Releases' },
  { id: 'keys', label: 'SDK keys' },
  { id: 'sdk', label: 'SDK setup' },
  { id: 'settings', label: 'Settings' },
];

export function ProjectDetailPage() {
  const { id = '' } = useParams();
  const { can } = useAuth();
  const { resyncToken } = useConnection();
  const project = useAsync(() => endpoints.getProject(id), [id, resyncToken]);
  const [tab, setTab] = useState<Tab>('overview');

  if (project.loading && !project.data) return <Spinner label="Loading project…" />;
  if (project.error && !project.data) return <ErrorMessage error={project.error} />;
  if (!project.data) return null;
  const p = project.data;

  const visibleTabs = TABS.filter((t) => t.id !== 'keys' || can('projectKeys'));

  return (
    <>
      <PageHeader
        title={p.name}
        subtitle={
          <span className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-xs">{p.repoPath || p.gitUrl}</span>
            {p.archived ? <Badge tone="warning">Archived</Badge> : null}
            {p.isSelf ? <Badge tone="accent">rhymehub itself</Badge> : null}
          </span>
        }
      />
      <ErrorMessage error={project.error} />

      <div role="tablist" aria-label="Project sections" className="mb-4 flex flex-wrap gap-1 border-b border-slate-200 dark:border-slate-700">
        {visibleTabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => setTab(t.id)}
            className={`-mb-px border-b-2 px-3 py-2 text-sm ${
              tab === t.id ? 'border-indigo-600 font-medium text-indigo-700 dark:text-indigo-300' : 'border-transparent text-slate-500'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === 'overview' ? <Overview project={p} onStats={() => project.reload()} /> : null}
      {tab === 'tasks' ? <TasksTab projectId={p.id} /> : null}
      {tab === 'requests' ? <RequestsTab projectId={p.id} /> : null}
      {tab === 'releases' ? <ReleasesTab projectId={p.id} /> : null}
      {tab === 'keys' ? <KeysTab projectId={p.id} /> : null}
      {tab === 'sdk' ? <SdkSetup projectId={p.id} /> : null}
      {tab === 'settings' ? <SettingsTab project={p} onSaved={(next) => project.setData(next)} /> : null}
    </>
  );
}

function Overview({ project, onStats }: { project: Project; onStats: () => void }) {
  const git = useAsync(() => endpoints.projectGit(project.id), [project.id]);
  useStreamEvent('task_updated', () => onStats());
  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Card title="Status">
        <dl className="grid grid-cols-2 gap-y-2 text-sm">
          <dt className="text-slate-500">Open requests</dt>
          <dd>{project.stats.openRequests}</dd>
          <dt className="text-slate-500">Active tasks</dt>
          <dd>{project.stats.activeTasks}</dd>
          <dt className="text-slate-500">Ready for review</dt>
          <dd>{project.stats.reviewReady}</dd>
          <dt className="text-slate-500">Failed</dt>
          <dd>{project.stats.failed}</dd>
          <dt className="text-slate-500">Default agent</dt>
          <dd>{project.defaultAgent || '–'}</dd>
          <dt className="text-slate-500">Release strategy</dt>
          <dd>{project.releaseStrategy}</dd>
          <dt className="text-slate-500">Auto-approve</dt>
          <dd>{project.autoApprove ? 'On' : 'Off'}</dd>
          <dt className="text-slate-500">Auto-release</dt>
          <dd>{project.autoRelease ? 'On' : 'Off'}</dd>
        </dl>
      </Card>
      <Card title="Git">
        {git.loading && !git.data ? <Spinner /> : null}
        <ErrorMessage error={git.error} />
        {git.data ? (
          <>
            <p className="text-sm">
              <span className="font-mono">{git.data.branch}</span> @ <span className="font-mono">{git.data.head.slice(0, 10)}</span>
              {git.data.dirty ? <Badge tone="warning">dirty</Badge> : null}
            </p>
            <ul className="mt-3 divide-y divide-slate-100 text-sm dark:divide-slate-800">
              {git.data.recentCommits.map((c) => (
                <li key={c.hash} className="py-1.5">
                  <span className="font-mono text-xs text-slate-500">{c.hash.slice(0, 8)}</span> {c.subject}
                  <span className="text-xs text-slate-400"> · {c.author} · {formatRelative(c.at)}</span>
                </li>
              ))}
            </ul>
          </>
        ) : null}
      </Card>
    </div>
  );
}

function TasksTab({ projectId }: { projectId: string }) {
  const { can } = useAuth();
  const { resyncToken } = useConnection();
  const tasks = useAsync(() => endpoints.listTasks({ projectId, limit: 100 }), [projectId, resyncToken]);
  useStreamEvent('task_updated', () => tasks.reload());
  return (
    <div className="flex flex-col gap-4">
      {can('createTask') ? <NewTaskForm projectId={projectId} onCreated={() => tasks.reload()} /> : null}
      <Card>
        <ErrorMessage error={tasks.error} />
        {tasks.loading && !tasks.data ? <Spinner /> : null}
        {tasks.data && tasks.data.length === 0 ? <Empty>No tasks for this project yet.</Empty> : null}
        <TaskList tasks={tasks.data ?? []} />
      </Card>
    </div>
  );
}

function TaskList({ tasks }: { tasks: TaskSummary[] }) {
  return (
    <ul className="divide-y divide-slate-100 dark:divide-slate-800">
      {tasks.map((t) => (
        <li key={t.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
          <Link to={`/tasks/${t.id}`} className="truncate text-sm text-indigo-700 hover:underline dark:text-indigo-300">
            {t.title}
          </Link>
          <span className="flex items-center gap-2">
            <StateBadge state={t.state} />
            <span className="text-xs text-slate-400">{formatRelative(t.updatedAt)}</span>
          </span>
        </li>
      ))}
    </ul>
  );
}

function NewTaskForm({ projectId, onCreated }: { projectId: string; onCreated: () => void }) {
  const navigate = useNavigate();
  const [prompt, setPrompt] = useState('');
  const [title, setTitle] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const task = await endpoints.createTask({ projectId, prompt: prompt.trim(), title: title.trim() || undefined });
      onCreated();
      navigate(`/tasks/${task.id}`);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="New task">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Field id="task-title" label="Title (optional)">
          <Input id="task-title" value={title} onChange={(e) => setTitle(e.target.value)} />
        </Field>
        <Field id="task-prompt" label="What should the agent do?">
          <Textarea id="task-prompt" rows={3} required value={prompt} onChange={(e) => setPrompt(e.target.value)} />
        </Field>
        <ErrorMessage error={error} />
        <div>
          <Button type="submit" variant="primary" disabled={busy || !prompt.trim()}>
            Start task
          </Button>
        </div>
      </form>
    </Card>
  );
}

function RequestsTab({ projectId }: { projectId: string }) {
  const { can } = useAuth();
  const { resyncToken } = useConnection();
  const requests = useAsync(() => endpoints.listRequests({ projectId, limit: 100 }), [projectId, resyncToken]);
  const [actionError, setActionError] = useState<string | null>(null);
  useStreamEvent('request_updated', () => requests.reload());

  const act = async (fn: () => Promise<unknown>) => {
    setActionError(null);
    try {
      await fn();
      requests.reload();
    } catch (err) {
      setActionError(errorMessage(err));
    }
  };

  return (
    <Card>
      <ErrorMessage error={requests.error ?? actionError} />
      {requests.loading && !requests.data ? <Spinner /> : null}
      {requests.data && requests.data.length === 0 ? <Empty>No requests for this project.</Empty> : null}
      <ul className="divide-y divide-slate-100 dark:divide-slate-800">
        {requests.data?.map((r: Request) => (
          <li key={r.id} className="flex flex-col gap-2 py-3 md:flex-row md:items-center md:justify-between">
            <div className="min-w-0">
              <p className="font-medium">{r.title}</p>
              <p className="text-xs text-slate-500">
                {r.type} · {priorityLabel(r.priority)} · {r.status} · {formatRelative(r.createdAt)}
              </p>
            </div>
            {can('manageRequests') ? (
              <div className="flex flex-wrap gap-2">
                {r.status === 'new' ? (
                  <Button onClick={() => void act(() => endpoints.updateRequest(r.id, { status: 'triaged' }))}>Triage</Button>
                ) : null}
                {r.status === 'triaged' || r.status === 'new' ? (
                  <Button variant="primary" onClick={() => void act(() => endpoints.startRequestTask(r.id))}>
                    Start task
                  </Button>
                ) : null}
                {r.taskId ? (
                  <Link to={`/tasks/${r.taskId}`} className="self-center text-sm text-indigo-700 hover:underline dark:text-indigo-300">
                    View task
                  </Link>
                ) : null}
              </div>
            ) : null}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function ReleasesTab({ projectId }: { projectId: string }) {
  const { can } = useAuth();
  const { resyncToken } = useConnection();
  const releases = useAsync(() => endpoints.listReleases(projectId, 50), [projectId, resyncToken]);
  const [version, setVersion] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await endpoints.createProjectRelease(projectId, { version: version.trim() || undefined, notes: notes.trim() || undefined });
      setVersion('');
      setNotes('');
      releases.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-4">
      {can('release') ? (
        <Card title="Cut a release">
          <form className="flex flex-col gap-3" onSubmit={(e) => void create(e)}>
            <Field id="release-version" label="Version (optional)" hint="Leave empty to let rhymehub pick the next version.">
              <Input id="release-version" value={version} onChange={(e) => setVersion(e.target.value)} />
            </Field>
            <Field id="release-notes" label="Notes">
              <Textarea id="release-notes" rows={3} value={notes} onChange={(e) => setNotes(e.target.value)} />
            </Field>
            <ErrorMessage error={error} />
            <div>
              <Button type="submit" variant="primary" disabled={busy}>
                Create release
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <ReleaseList releases={releases.data ?? []} loading={releases.loading && !releases.data} error={releases.error} />
    </div>
  );
}

export function ReleaseList({ releases, loading, error }: { releases: Release[]; loading: boolean; error: string | null }) {
  return (
    <Card>
      <ErrorMessage error={error} />
      {loading ? <Spinner /> : null}
      {!loading && releases.length === 0 ? <Empty>No releases yet.</Empty> : null}
      <ul className="divide-y divide-slate-100 dark:divide-slate-800">
        {releases.map((r) => (
          <li key={r.id} className="py-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-medium">{r.version || r.tag}</span>
              <span className="flex items-center gap-2">
                <Badge tone={r.status === 'published' ? 'success' : r.status === 'failed' ? 'danger' : 'neutral'}>{r.status}</Badge>
                <span className="text-xs text-slate-400">{formatRelative(r.createdAt)}</span>
              </span>
            </div>
            {r.notes ? <p className="mt-1 whitespace-pre-line text-sm text-slate-600 dark:text-slate-300">{r.notes}</p> : null}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function KeysTab({ projectId }: { projectId: string }) {
  const { can } = useAuth();
  const keys = useAsync(() => endpoints.listProjectKeys(projectId), [projectId]);
  const [name, setName] = useState('');
  const [origins, setOrigins] = useState('');
  const [created, setCreated] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const allowedOrigins = origins
        .split(',')
        .map((o) => o.trim())
        .filter(Boolean);
      const key = await endpoints.createProjectKey(projectId, { name: name.trim(), allowedOrigins });
      setCreated(key.key);
      setName('');
      setOrigins('');
      keys.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (key: ProjectKey) => {
    setError(null);
    try {
      await endpoints.deleteProjectKey(projectId, key.id);
      keys.reload();
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  if (!can('projectKeys')) return <Empty>Maintainer access is required to manage SDK keys.</Empty>;

  return (
    <div className="flex flex-col gap-4">
      <Card title="Create SDK key">
        <form className="flex flex-col gap-3" onSubmit={(e) => void create(e)}>
          <Field id="key-name" label="Name">
            <Input id="key-name" required value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field id="key-origins" label="Allowed origins" hint="Comma-separated. Leave empty for server-side use only.">
            <Input id="key-origins" value={origins} onChange={(e) => setOrigins(e.target.value)} />
          </Field>
          <ErrorMessage error={error} />
          <div>
            <Button type="submit" variant="primary" disabled={busy || !name.trim()}>
              Create key
            </Button>
          </div>
        </form>
        {created ? (
          <div className="mt-4 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm dark:border-amber-700 dark:bg-amber-950">
            <p className="font-medium">Copy this key now. It will not be shown again.</p>
            <code className="mt-2 block break-all font-mono text-xs">{created}</code>
          </div>
        ) : null}
      </Card>
      <Card title="Keys">
        <ErrorMessage error={keys.error} />
        {keys.loading && !keys.data ? <Spinner /> : null}
        {keys.data && keys.data.length === 0 ? <Empty>No SDK keys.</Empty> : null}
        <ul className="divide-y divide-slate-100 dark:divide-slate-800">
          {keys.data?.map((k) => (
            <li key={k.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
              <div>
                <p className="font-medium">{k.name}</p>
                <p className="font-mono text-xs text-slate-500">{k.keyPrefix}…</p>
              </div>
              {k.revokedAt ? (
                <Badge>Revoked</Badge>
              ) : (
                <Button variant="danger" onClick={() => void revoke(k)}>
                  Revoke
                </Button>
              )}
            </li>
          ))}
        </ul>
      </Card>
    </div>
  );
}

const STRATEGIES: ReleaseStrategy[] = ['none', 'tag', 'command', 'rhymehub-self'];

function SettingsTab({ project, onSaved }: { project: Project; onSaved: (p: Project) => void }) {
  const { can } = useAuth();
  const [form, setForm] = useState({
    name: project.name,
    defaultBranch: project.defaultBranch,
    buildCmd: project.buildCmd,
    testCmd: project.testCmd,
    releaseStrategy: project.releaseStrategy,
    releaseCmd: project.releaseCmd,
    maxConcurrent: String(project.maxConcurrent),
    timeoutMinutes: String(project.timeoutMinutes),
    autoTaskFromRequests: project.autoTaskFromRequests,
    autoApprove: project.autoApprove,
    autoRelease: project.autoRelease,
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const canPolicy = can('projectPolicies');

  const set = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) => setForm((f) => ({ ...f, [key]: value }));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const body: Partial<Project> = {
        name: form.name.trim(),
        defaultBranch: form.defaultBranch.trim(),
        buildCmd: form.buildCmd,
        testCmd: form.testCmd,
        releaseStrategy: form.releaseStrategy,
        releaseCmd: form.releaseCmd,
        maxConcurrent: Number(form.maxConcurrent),
        timeoutMinutes: Number(form.timeoutMinutes),
        autoTaskFromRequests: form.autoTaskFromRequests,
      };
      if (canPolicy) {
        body.autoApprove = form.autoApprove;
        body.autoRelease = form.autoRelease;
      }
      onSaved(await endpoints.updateProject(project.id, body));
      setSaved(true);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  if (!can('createProject') && !canPolicy) return <Empty>Developer access is required to edit project settings.</Empty>;

  return (
    <Card>
      <form className="grid gap-4 md:grid-cols-2" onSubmit={(e) => void submit(e)}>
        <Field id="s-name" label="Name">
          <Input id="s-name" value={form.name} onChange={(e) => set('name', e.target.value)} />
        </Field>
        <Field id="s-branch" label="Default branch">
          <Input id="s-branch" value={form.defaultBranch} onChange={(e) => set('defaultBranch', e.target.value)} />
        </Field>
        <Field id="s-build" label="Build command">
          <Input id="s-build" value={form.buildCmd} onChange={(e) => set('buildCmd', e.target.value)} />
        </Field>
        <Field id="s-test" label="Test command">
          <Input id="s-test" value={form.testCmd} onChange={(e) => set('testCmd', e.target.value)} />
        </Field>
        <Field id="s-strategy" label="Release strategy">
          <Select id="s-strategy" value={form.releaseStrategy} onChange={(e) => set('releaseStrategy', e.target.value as ReleaseStrategy)}>
            {STRATEGIES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="s-release-cmd" label="Release command">
          <Input id="s-release-cmd" value={form.releaseCmd} onChange={(e) => set('releaseCmd', e.target.value)} />
        </Field>
        <Field id="s-max" label="Max concurrent tasks">
          <Input id="s-max" type="number" min={1} value={form.maxConcurrent} onChange={(e) => set('maxConcurrent', e.target.value)} />
        </Field>
        <Field id="s-timeout" label="Timeout (minutes)">
          <Input id="s-timeout" type="number" min={1} value={form.timeoutMinutes} onChange={(e) => set('timeoutMinutes', e.target.value)} />
        </Field>
        <label className="flex items-center gap-2 text-sm md:col-span-2">
          <input type="checkbox" checked={form.autoTaskFromRequests} onChange={(e) => set('autoTaskFromRequests', e.target.checked)} />
          Start tasks automatically from new requests
        </label>
        {canPolicy ? (
          <fieldset className="flex flex-col gap-2 rounded-md border border-slate-200 p-3 md:col-span-2 dark:border-slate-700">
            <legend className="px-1 text-sm font-medium">Maintainer policies</legend>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={form.autoApprove} onChange={(e) => set('autoApprove', e.target.checked)} />
              Auto-approve tasks that pass validation
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={form.autoRelease} onChange={(e) => set('autoRelease', e.target.checked)} />
              Auto-release after approval
            </label>
          </fieldset>
        ) : null}
        <div className="flex items-center gap-3 md:col-span-2">
          <Button type="submit" variant="primary" disabled={busy}>
            Save settings
          </Button>
          {saved ? <span className="text-sm text-emerald-700 dark:text-emerald-300">Saved.</span> : null}
        </div>
        <div className="md:col-span-2">
          <ErrorMessage error={error} />
        </div>
      </form>
    </Card>
  );
}
