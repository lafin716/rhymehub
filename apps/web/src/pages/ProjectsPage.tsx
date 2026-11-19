import { useState } from 'react';
import { Link } from 'react-router';
import { endpoints } from '../api/endpoints';
import { Button, Card, Empty, ErrorMessage, Input, PageHeader, Spinner } from '../components/ui';
import { formatRelative } from '../lib/format';
import { useAuth } from '../state/AuthContext';
import { useConnection } from '../state/ConnectionContext';
import { useAsync } from '../state/useApi';

export function ProjectsPage() {
  const { can } = useAuth();
  const { resyncToken } = useConnection();
  const [query, setQuery] = useState('');
  const [submitted, setSubmitted] = useState('');
  const projects = useAsync(() => endpoints.listProjects(submitted || undefined), [submitted, resyncToken]);

  return (
    <>
      <PageHeader
        title="Projects"
        actions={
          can('createProject') ? (
            <Link to="/projects/new">
              <Button variant="primary">Register project</Button>
            </Link>
          ) : null
        }
      />
      <form
        className="mb-4 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted(query.trim());
        }}
      >
        <Input aria-label="Search projects" placeholder="Search by name" value={query} onChange={(e) => setQuery(e.target.value)} />
        <Button type="submit">Search</Button>
      </form>
      <ErrorMessage error={projects.error} />
      {projects.loading && !projects.data ? <Spinner /> : null}
      {projects.data && projects.data.length === 0 ? <Empty>No matching projects.</Empty> : null}
      <div className="grid gap-3 md:grid-cols-2">
        {projects.data?.map((project) => (
          <Card key={project.id}>
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <Link to={`/projects/${project.id}`} className="font-semibold text-indigo-700 hover:underline dark:text-indigo-300">
                  {project.name}
                </Link>
                <p className="truncate text-xs text-slate-500 dark:text-slate-400">{project.repoPath || project.gitUrl}</p>
              </div>
              <span className="shrink-0 text-xs text-slate-400">{formatRelative(project.updatedAt)}</span>
            </div>
            <p className="mt-3 text-sm text-slate-600 dark:text-slate-300">
              {project.stats.openRequests} open requests · {project.stats.activeTasks} active tasks · {project.stats.reviewReady} ready for review
            </p>
          </Card>
        ))}
      </div>
    </>
  );
}
