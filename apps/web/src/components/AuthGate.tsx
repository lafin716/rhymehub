import { Navigate, useLocation } from 'react-router';
import { useAuth } from '../state/AuthContext';
import { ConnectionProvider } from '../state/ConnectionContext';
import { Layout } from './Layout';
import { Button, ErrorMessage, Spinner } from './ui';

/** Layout route for every signed-in screen: redirects to setup/login, otherwise mounts live updates. */
export function AuthGate() {
  const { status, error, refresh } = useAuth();
  const location = useLocation();

  if (status === 'loading') {
    return (
      <div className="p-8">
        <Spinner label="Connecting to rhymehub…" />
      </div>
    );
  }
  if (status === 'error') {
    return (
      <div className="flex flex-col gap-3 p-8">
        <ErrorMessage error={error ?? 'Could not reach rhymehub.'} />
        <div>
          <Button onClick={() => void refresh()}>Try again</Button>
        </div>
      </div>
    );
  }
  if (status === 'setup') return <Navigate to="/setup" replace />;
  if (status === 'anonymous') {
    return <Navigate to="/login" replace state={{ from: `${location.pathname}${location.search}` }} />;
  }

  return (
    <ConnectionProvider>
      <Layout />
    </ConnectionProvider>
  );
}
