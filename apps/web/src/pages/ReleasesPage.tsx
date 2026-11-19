import { useState } from 'react';
import { endpoints } from '../api/endpoints';
import { Field, PageHeader, Select } from '../components/ui';
import { useConnection } from '../state/ConnectionContext';
import { useAsync } from '../state/useApi';
import { ReleaseList } from './ProjectDetailPage';

const STATUS_FILTERS = ['', 'building', 'published', 'failed', 'rolled_back'] as const;

export function ReleasesPage() {
  const { resyncToken } = useConnection();
  const [status, setStatus] = useState<(typeof STATUS_FILTERS)[number]>('');
  const releases = useAsync(() => endpoints.listReleases(undefined, 100), [resyncToken]);
  const visible = (releases.data ?? []).filter((r) => !status || r.status === status);

  return (
    <>
      <PageHeader title="Releases" subtitle="Releases across all projects, newest first." />
      <div className="mb-4 max-w-xs">
        <Field id="release-status" label="Status">
          <Select id="release-status" value={status} onChange={(e) => setStatus(e.target.value as (typeof STATUS_FILTERS)[number])}>
            {STATUS_FILTERS.map((s) => (
              <option key={s || 'all'} value={s}>
                {s || 'All statuses'}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      <ReleaseList releases={visible} loading={releases.loading && !releases.data} error={releases.error} />
    </>
  );
}
