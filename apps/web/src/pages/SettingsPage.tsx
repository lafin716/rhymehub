import { useState, type FormEvent } from 'react';
import { errorMessage } from '../api/client';
import { endpoints } from '../api/endpoints';
import type { ApiToken, Role, User, UpdateRecord } from '../api/types';
import { Badge, Button, Card, Empty, ErrorMessage, Field, Input, PageHeader, Select, Spinner } from '../components/ui';
import { ROLE_LABEL, ROLE_ORDER } from '../lib/roles';
import { formatDateTime, formatRelative } from '../lib/format';
import { currentPushEndpoint, disablePush, enablePush, pushSupported } from '../lib/push';
import { useAuth } from '../state/AuthContext';
import { useConnection } from '../state/ConnectionContext';
import { useAsync } from '../state/useApi';

export function SettingsPage() {
  const { can } = useAuth();
  return (
    <>
      <PageHeader title="Settings" subtitle="Your access tokens, notifications, and server status." />
      <div className="flex flex-col gap-4">
        <TokensCard />
        <PushCard />
        {can('manageUsers') ? <UsersCard /> : null}
        <SystemCard />
        {can('viewAudit') ? <AuditCard /> : null}
      </div>
    </>
  );
}

function TokensCard() {
  const { resyncToken } = useConnection();
  const tokens = useAsync(() => endpoints.listTokens(), [resyncToken]);
  const [name, setName] = useState('');
  const [created, setCreated] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const result = await endpoints.createToken(name.trim());
      setCreated(result.token);
      setName('');
      tokens.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (token: ApiToken) => {
    setError(null);
    try {
      await endpoints.revokeToken(token.id);
      tokens.reload();
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  return (
    <Card title="API tokens">
      <form className="flex flex-wrap items-end gap-2" onSubmit={(e) => void create(e)}>
        <div className="min-w-[14rem] flex-1">
          <Field id="token-name" label="Token name">
            <Input id="token-name" required value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
        </div>
        <Button type="submit" variant="primary" disabled={busy || !name.trim()}>
          Create token
        </Button>
      </form>
      {created ? (
        <div className="mt-4 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm dark:border-amber-700 dark:bg-amber-950">
          <p className="font-medium">Copy this token now. It will not be shown again.</p>
          <code className="mt-2 block break-all font-mono text-xs">{created}</code>
        </div>
      ) : null}
      <div className="mt-4">
        <ErrorMessage error={error ?? tokens.error} />
        {tokens.loading && !tokens.data ? <Spinner /> : null}
        {tokens.data && tokens.data.length === 0 ? <Empty>No tokens.</Empty> : null}
        <ul className="divide-y divide-slate-100 dark:divide-slate-800">
          {tokens.data?.map((t) => (
            <li key={t.id} className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm">
              <div>
                <p className="font-medium">{t.name}</p>
                <p className="text-xs text-slate-500">
                  Created {formatDateTime(t.createdAt)}
                  {t.lastUsedAt ? ` · last used ${formatRelative(t.lastUsedAt)}` : ''}
                </p>
              </div>
              {t.revokedAt ? (
                <Badge>Revoked</Badge>
              ) : (
                <Button variant="danger" onClick={() => void revoke(t)}>
                  Revoke
                </Button>
              )}
            </li>
          ))}
        </ul>
      </div>
    </Card>
  );
}

function PushCard() {
  const supported = pushSupported();
  const endpoint = useAsync(() => (supported ? currentPushEndpoint() : Promise.resolve(null)), [supported]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const run = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await fn();
      setNotice(done);
      endpoint.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const subscribed = Boolean(endpoint.data);

  return (
    <Card title="Push notifications">
      {!supported ? (
        <Empty>This browser does not support web push. Install rhymehub as a PWA on a supported browser.</Empty>
      ) : (
        <div className="flex flex-col gap-3 text-sm">
          <p className="text-slate-600 dark:text-slate-300">
            {subscribed ? 'This device receives push notifications.' : 'This device is not subscribed to push notifications.'}
          </p>
          <div className="flex flex-wrap gap-2">
            {!subscribed ? (
              <Button variant="primary" disabled={busy} onClick={() => void run(enablePush, 'Push enabled.')}>
                Enable push
              </Button>
            ) : (
              <>
                <Button disabled={busy} onClick={() => void run(() => endpoints.testPush(), 'Test push sent.')}>
                  Send test
                </Button>
                <Button variant="danger" disabled={busy} onClick={() => void run(disablePush, 'Push disabled.')}>
                  Disable push
                </Button>
              </>
            )}
          </div>
          {notice ? <p className="text-emerald-700 dark:text-emerald-300">{notice}</p> : null}
          <ErrorMessage error={error ?? endpoint.error} />
        </div>
      )}
    </Card>
  );
}

function UsersCard() {
  const { user: me } = useAuth();
  const { resyncToken } = useConnection();
  const users = useAsync(() => endpoints.listUsers(), [resyncToken]);
  const [error, setError] = useState<string | null>(null);
  const [form, setForm] = useState<{ username: string; password: string; role: Role }>({
    username: '',
    password: '',
    role: 'developer',
  });
  const [busy, setBusy] = useState(false);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await endpoints.createUser(form);
      setForm({ username: '', password: '', role: 'developer' });
      users.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const update = async (target: User, body: { role?: Role; disabled?: boolean }) => {
    setError(null);
    try {
      await endpoints.updateUser(target.id, body);
      users.reload();
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  return (
    <Card title="Users">
      <form className="mb-4 grid gap-2 md:grid-cols-[1fr_1fr_auto_auto] md:items-end" onSubmit={(e) => void create(e)}>
        <Field id="user-name" label="Username">
          <Input id="user-name" required value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} />
        </Field>
        <Field id="user-pass" label="Temporary password" hint="At least 8 characters.">
          <Input
            id="user-pass"
            type="password"
            required
            minLength={8}
            autoComplete="new-password"
            value={form.password}
            onChange={(e) => setForm({ ...form, password: e.target.value })}
          />
        </Field>
        <Field id="user-role" label="Role">
          <Select id="user-role" value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value as Role })}>
            {ROLE_ORDER.map((r) => (
              <option key={r} value={r}>
                {ROLE_LABEL[r]}
              </option>
            ))}
          </Select>
        </Field>
        <Button type="submit" variant="primary" disabled={busy}>
          Add user
        </Button>
      </form>
      <ErrorMessage error={error ?? users.error} />
      {users.loading && !users.data ? <Spinner /> : null}
      <ul className="divide-y divide-slate-100 dark:divide-slate-800">
        {users.data?.map((u) => (
          <li key={u.id} className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm">
            <div className="flex items-center gap-2">
              <span className="font-medium">{u.username}</span>
              {u.id === me?.id ? <Badge tone="info">you</Badge> : null}
              {u.disabled ? <Badge tone="warning">disabled</Badge> : null}
            </div>
            <div className="flex items-center gap-2">
              <Select
                aria-label={`Role for ${u.username}`}
                className="w-auto"
                value={u.role}
                disabled={u.id === me?.id}
                onChange={(e) => void update(u, { role: e.target.value as Role })}
              >
                {ROLE_ORDER.map((r) => (
                  <option key={r} value={r}>
                    {ROLE_LABEL[r]}
                  </option>
                ))}
              </Select>
              {u.id !== me?.id ? (
                <Button variant="ghost" onClick={() => void update(u, { disabled: !u.disabled })}>
                  {u.disabled ? 'Enable' : 'Disable'}
                </Button>
              ) : null}
            </div>
          </li>
        ))}
      </ul>
    </Card>
  );
}

function SystemCard() {
  const { can } = useAuth();
  const { resyncToken } = useConnection();
  const status = useAsync(() => endpoints.systemStatus(), [resyncToken]);
  const updates = useAsync(() => endpoints.systemUpdates(), [resyncToken]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const run = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await fn();
      setNotice(done);
      updates.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const install = (version: string) => {
    if (!window.confirm(`Install rhymehub ${version}? The server will restart.`)) return;
    void run(() => endpoints.startUpdate(version), `Update to ${version} started. Waiting for the server to come back.`);
  };

  const rollback = (record: UpdateRecord) => {
    if (!window.confirm(`Roll back from ${record.to} to ${record.from}? The server will restart.`)) return;
    void run(() => endpoints.rollbackUpdate(record.id), `Rollback from ${record.to} started.`);
  };

  return (
    <Card title="System">
      <ErrorMessage error={status.error} />
      {status.data ? (
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm md:grid-cols-4">
          <dt className="text-slate-500">Version</dt>
          <dd>{status.data.version}</dd>
          <dt className="text-slate-500">Schema</dt>
          <dd>{status.data.schemaVersion}</dd>
          <dt className="text-slate-500">Uptime</dt>
          <dd>{Math.round(status.data.uptimeSec / 60)} min</dd>
          <dt className="text-slate-500">Active workers</dt>
          <dd>{status.data.activeWorkers}</dd>
          <dt className="text-slate-500">Draining</dt>
          <dd>{status.data.draining ? 'yes' : 'no'}</dd>
          <dt className="text-slate-500">Supervisor</dt>
          <dd>{status.data.supervisor.reachable ? `reachable (${status.data.supervisor.version})` : 'unreachable'}</dd>
          <dt className="text-slate-500">Update in progress</dt>
          <dd>{status.data.supervisor.updateInProgress ? 'yes' : 'no'}</dd>
        </dl>
      ) : status.loading ? (
        <Spinner />
      ) : null}

      <h3 className="mt-6 text-sm font-semibold">Available updates</h3>
      <ErrorMessage error={updates.error} />
      {updates.data && updates.data.available.length === 0 ? <Empty>You are up to date.</Empty> : null}
      <ul className="mt-2 divide-y divide-slate-100 dark:divide-slate-800">
        {updates.data?.available.map((u) => (
          <li key={u.version} className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm">
            <div>
              <p className="font-medium">{u.version}</p>
              {u.notes ? <p className="whitespace-pre-line text-xs text-slate-500">{u.notes}</p> : null}
            </div>
            {can('systemUpdate') ? (
              <Button variant="primary" disabled={busy} onClick={() => install(u.version)}>
                Install
              </Button>
            ) : null}
          </li>
        ))}
      </ul>

      <h3 className="mt-6 text-sm font-semibold">Update history</h3>
      {updates.data && updates.data.history.length === 0 ? <Empty>No updates recorded.</Empty> : null}
      <ul className="mt-2 divide-y divide-slate-100 dark:divide-slate-800">
        {updates.data?.history.map((h) => (
          <li key={h.id} className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm">
            <div>
              <p>
                {h.from} → {h.to} <Badge tone={h.status === 'succeeded' ? 'success' : h.status === 'failed' ? 'danger' : 'warning'}>{h.status}</Badge>
              </p>
              <p className="text-xs text-slate-500">
                {formatDateTime(h.startedAt)} · phase {h.phase || '–'}
                {h.error ? ` · ${h.error}` : ''}
              </p>
            </div>
            {can('systemUpdate') && h.status === 'succeeded' ? (
              <Button variant="danger" disabled={busy} onClick={() => rollback(h)}>
                Roll back
              </Button>
            ) : null}
          </li>
        ))}
      </ul>
      <div className="mt-3 text-sm">
        {notice ? <p className="text-emerald-700 dark:text-emerald-300">{notice}</p> : null}
        <ErrorMessage error={error} />
      </div>
    </Card>
  );
}

function AuditCard() {
  const { resyncToken } = useConnection();
  const audit = useAsync(() => endpoints.listAudit(100), [resyncToken]);
  return (
    <Card title="Audit log">
      <ErrorMessage error={audit.error} />
      {audit.loading && !audit.data ? <Spinner /> : null}
      {audit.data && audit.data.length === 0 ? <Empty>No audit entries.</Empty> : null}
      <div className="max-h-96 overflow-auto">
        <table className="w-full text-left text-xs">
          <thead className="sticky top-0 bg-white text-slate-500 dark:bg-slate-900">
            <tr>
              <th className="py-1 pr-3 font-medium">When</th>
              <th className="py-1 pr-3 font-medium">Actor</th>
              <th className="py-1 pr-3 font-medium">Action</th>
              <th className="py-1 pr-3 font-medium">Target</th>
              <th className="py-1 font-medium">Detail</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {audit.data?.map((entry) => (
              <tr key={entry.id}>
                <td className="whitespace-nowrap py-1 pr-3 text-slate-500">{formatDateTime(entry.at)}</td>
                <td className="py-1 pr-3">
                  {entry.actor} <span className="text-slate-400">({entry.actorRole})</span>
                </td>
                <td className="py-1 pr-3 font-mono">{entry.action}</td>
                <td className="py-1 pr-3 font-mono">
                  {entry.targetType}:{entry.targetId}
                </td>
                <td className="py-1 break-all">{entry.detail}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}
