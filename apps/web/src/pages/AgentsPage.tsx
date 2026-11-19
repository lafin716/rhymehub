import { useEffect, useState, type FormEvent } from 'react';
import { errorMessage } from '../api/client';
import { endpoints } from '../api/endpoints';
import type { AgentInfo, AgentLimits } from '../api/types';
import { Badge, Button, Card, Empty, ErrorMessage, Field, Input, PageHeader, Spinner } from '../components/ui';
import { useAuth } from '../state/AuthContext';
import { useConnection } from '../state/ConnectionContext';
import { useAsync } from '../state/useApi';

export function AgentsPage() {
  const { can } = useAuth();
  const { resyncToken } = useConnection();
  const data = useAsync(() => endpoints.listAgents(), [resyncToken]);

  return (
    <>
      <PageHeader title="Agents" subtitle="Local coding agents rhymehub can dispatch tasks to." />
      <ErrorMessage error={data.error} />
      {data.loading && !data.data ? <Spinner /> : null}
      {data.data ? (
        <div className="flex flex-col gap-4">
          <Card title="Agents">
            {data.data.agents.length === 0 ? <Empty>No agents detected.</Empty> : null}
            <ul className="divide-y divide-slate-100 dark:divide-slate-800">
              {data.data.agents.map((agent) => (
                <AgentRow key={agent.name} agent={agent} />
              ))}
            </ul>
          </Card>
          {can('agentLimits') ? (
            <LimitsForm limits={data.data.limits} agents={data.data.agents} onSaved={() => data.reload()} />
          ) : (
            <Card title="Concurrency limits">
              <p className="text-sm text-slate-600 dark:text-slate-300">
                Global limit {data.data.limits.global}. Administrator access is required to change limits.
              </p>
            </Card>
          )}
        </div>
      ) : null}
    </>
  );
}

function AgentRow({ agent }: { agent: AgentInfo }) {
  const caps = agent.capabilities;
  return (
    <li className="flex flex-col gap-2 py-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="font-medium">{agent.name}</span>
          <Badge tone={agent.available ? 'success' : 'danger'}>{agent.available ? 'available' : 'unavailable'}</Badge>
          {agent.version ? <span className="font-mono text-xs text-slate-500">{agent.version}</span> : null}
        </div>
        <span className="text-sm text-slate-600 dark:text-slate-300">
          {agent.running} / {agent.limit} running
        </span>
      </div>
      {agent.error ? <p className="text-xs text-rose-700 dark:text-rose-300">{agent.error}</p> : null}
      <div className="flex flex-wrap gap-1 text-xs text-slate-500 dark:text-slate-400">
        {caps.streaming ? <Badge>streaming</Badge> : null}
        {caps.resume ? <Badge>resume</Badge> : null}
        {caps.permissionRequests ? <Badge>permissions</Badge> : null}
        {caps.cancel ? <Badge>cancel</Badge> : null}
        {caps.costReporting ? <Badge>cost</Badge> : null}
        {caps.models.map((m) => (
          <Badge key={m}>{m}</Badge>
        ))}
      </div>
    </li>
  );
}

function LimitsForm({ limits, agents, onSaved }: { limits: AgentLimits; agents: AgentInfo[]; onSaved: () => void }) {
  const [global, setGlobal] = useState(String(limits.global));
  const [perAgent, setPerAgent] = useState<Record<string, string>>(() =>
    Object.fromEntries(agents.map((a) => [a.name, String(limits.perAgent[a.name] ?? '')])),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Re-sync the inputs when the server copy changes underneath us.
  useEffect(() => {
    setGlobal(String(limits.global));
  }, [limits.global]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const overrides: Record<string, number> = {};
      for (const [name, value] of Object.entries(perAgent)) {
        if (value.trim() !== '') overrides[name] = Number(value);
      }
      await endpoints.updateAgentLimits({ global: Number(global), perAgent: overrides });
      onSaved();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Concurrency limits">
      <form className="flex flex-col gap-4" onSubmit={(e) => void submit(e)}>
        <Field id="limit-global" label="Global maximum concurrent tasks">
          <Input id="limit-global" type="number" min={1} value={global} onChange={(e) => setGlobal(e.target.value)} />
        </Field>
        {agents.map((agent) => (
          <Field key={agent.name} id={`limit-${agent.name}`} label={`${agent.name} limit`} hint="Leave empty to use the global limit.">
            <Input
              id={`limit-${agent.name}`}
              type="number"
              min={1}
              value={perAgent[agent.name] ?? ''}
              onChange={(e) => setPerAgent((prev) => ({ ...prev, [agent.name]: e.target.value }))}
            />
          </Field>
        ))}
        <ErrorMessage error={error} />
        <div>
          <Button type="submit" variant="primary" disabled={busy}>
            Save limits
          </Button>
        </div>
      </form>
    </Card>
  );
}
