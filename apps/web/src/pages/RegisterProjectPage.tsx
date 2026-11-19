import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router';
import { errorMessage } from '../api/client';
import { endpoints } from '../api/endpoints';
import type { ProjectCandidate } from '../api/types';
import { Button, Card, Empty, ErrorMessage, Field, Input, PageHeader, Spinner } from '../components/ui';
import { useAuth } from '../state/AuthContext';

type Mode = 'path' | 'git' | 'discover';

const MODES: { id: Mode; label: string }[] = [
  { id: 'path', label: 'Local path' },
  { id: 'git', label: 'Git URL' },
  { id: 'discover', label: 'Discover' },
];

export function RegisterProjectPage() {
  const { can } = useAuth();
  const navigate = useNavigate();
  const [mode, setMode] = useState<Mode>('path');
  const [name, setName] = useState('');
  const [target, setTarget] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [candidates, setCandidates] = useState<ProjectCandidate[] | null>(null);

  if (!can('createProject')) {
    return (
      <>
        <PageHeader title="Register project" />
        <Empty>Developer access is required to register projects.</Empty>
      </>
    );
  }

  const create = async (body: { name: string; repoPath?: string; gitUrl?: string }) => {
    const project = await endpoints.createProject(body);
    navigate(`/projects/${project.id}`);
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (mode === 'discover') {
        setCandidates(await endpoints.discoverProjects(target.trim()));
      } else if (mode === 'path') {
        await create({ name: name.trim(), repoPath: target.trim() });
      } else {
        await create({ name: name.trim(), gitUrl: target.trim() });
      }
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const registerCandidate = async (candidate: ProjectCandidate) => {
    setBusy(true);
    setError(null);
    try {
      await create({ name: candidate.name, repoPath: candidate.path });
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader title="Register project" subtitle="Add a repository that rhymehub should manage." />
      <Card>
        <div role="tablist" aria-label="Registration method" className="mb-4 flex gap-1">
          {MODES.map((m) => (
            <Button
              key={m.id}
              role="tab"
              aria-selected={mode === m.id}
              variant={mode === m.id ? 'primary' : 'ghost'}
              onClick={() => {
                setMode(m.id);
                setCandidates(null);
                setError(null);
              }}
            >
              {m.label}
            </Button>
          ))}
        </div>

        <form className="flex flex-col gap-4" onSubmit={(e) => void submit(e)}>
          {mode !== 'discover' ? (
            <Field id="project-name" label="Name">
              <Input id="project-name" required value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
          ) : null}
          <Field
            id="project-target"
            label={mode === 'path' ? 'Local repository path' : mode === 'git' ? 'Git URL' : 'Folder to scan'}
            hint={
              mode === 'discover'
                ? 'rhymehub looks for git repositories under this folder on the server.'
                : mode === 'path'
                  ? 'Absolute path on the machine running rhymehub.'
                  : 'rhymehub clones this repository on the server.'
            }
          >
            <Input id="project-target" required value={target} onChange={(e) => setTarget(e.target.value)} />
          </Field>
          <ErrorMessage error={error} />
          <div>
            <Button type="submit" variant="primary" disabled={busy}>
              {mode === 'discover' ? 'Scan' : 'Register'}
            </Button>
          </div>
        </form>

        {mode === 'discover' && candidates ? (
          <div className="mt-6">
            {candidates.length === 0 ? <Empty>No repositories found under that folder.</Empty> : null}
            <ul className="divide-y divide-slate-100 dark:divide-slate-800">
              {candidates.map((c) => (
                <li key={c.path} className="flex flex-wrap items-center justify-between gap-2 py-2">
                  <div className="min-w-0">
                    <p className="font-medium">{c.name}</p>
                    <p className="truncate font-mono text-xs text-slate-500 dark:text-slate-400">{c.path}</p>
                  </div>
                  {c.registered ? (
                    <span className="text-xs text-slate-500">Already registered</span>
                  ) : (
                    <Button disabled={busy} onClick={() => void registerCandidate(c)}>
                      Register
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        {busy && mode === 'discover' && !candidates ? <Spinner label="Scanning…" /> : null}
      </Card>
    </>
  );
}
