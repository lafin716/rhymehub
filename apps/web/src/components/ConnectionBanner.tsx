import type { ReactNode } from 'react';
import { useConnection } from '../state/ConnectionContext';
import { Button } from './ui';

/** Shows server/stream trouble and the "server was updated, reload" prompt. */
export function ConnectionBanner() {
  const { stream, serverOnline, updateAvailable, reloadPage } = useConnection();

  if (updateAvailable) {
    return (
      <Banner tone="info">
        <span>rhymehub was updated and restarted. Reload to use the new version.</span>
        <Button variant="primary" onClick={reloadPage}>
          Reload
        </Button>
      </Banner>
    );
  }
  if (!serverOnline) {
    return <Banner tone="warning">Server unreachable. Retrying every few seconds. It may be restarting for an update.</Banner>;
  }
  if (stream === 'reconnecting') {
    return <Banner tone="warning">Live updates disconnected. Reconnecting…</Banner>;
  }
  return null;
}

function Banner({ tone, children }: { tone: 'info' | 'warning'; children: ReactNode }) {
  const style =
    tone === 'warning'
      ? 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100'
      : 'border-sky-300 bg-sky-50 text-sky-900 dark:border-sky-700 dark:bg-sky-950 dark:text-sky-100';
  return (
    <div role="status" className={`flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2 text-sm ${style}`}>
      {children}
    </div>
  );
}
