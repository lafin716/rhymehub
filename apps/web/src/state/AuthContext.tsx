import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { ApiError, errorMessage, setUnauthorizedHandler } from '../api/client';
import { endpoints } from '../api/endpoints';
import type { Role, User } from '../api/types';
import { can as hasCapability, type Capability } from '../lib/roles';

export type AuthStatus = 'loading' | 'setup' | 'anonymous' | 'authenticated' | 'error';

interface AuthState {
  status: AuthStatus;
  user: User | null;
  version: string;
  error: string | null;
}

export interface AuthValue extends AuthState {
  role: Role | null;
  can: (capability: Capability) => boolean;
  refresh: () => Promise<void>;
  login: (username: string, password: string) => Promise<void>;
  setup: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({ status: 'loading', user: null, version: '', error: null });

  const refresh = useCallback(async () => {
    setState((s) => ({ ...s, status: s.status === 'authenticated' ? s.status : 'loading', error: null }));
    let version = '';
    try {
      const meta = await endpoints.meta();
      version = meta.version;
      if (meta.needsSetup) {
        setState({ status: 'setup', user: null, version, error: null });
        return;
      }
      const { user } = await endpoints.me();
      setState({ status: 'authenticated', user, version, error: null });
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setState({ status: 'anonymous', user: null, version, error: null });
        return;
      }
      setState({ status: 'error', user: null, version, error: errorMessage(err) });
    }
  }, []);

  useEffect(() => {
    // Any 401 from a regular API call drops the session and sends the user to login.
    setUnauthorizedHandler(() =>
      setState((s) => (s.status === 'authenticated' ? { ...s, status: 'anonymous', user: null } : s)),
    );
    void refresh();
    return () => setUnauthorizedHandler(null);
  }, [refresh]);

  const login = useCallback(async (username: string, password: string) => {
    const { user } = await endpoints.login({ username, password });
    setState((s) => ({ ...s, status: 'authenticated', user, error: null }));
  }, []);

  // Assumption: POST /setup does not start a session, so we log in with the new account afterwards.
  const setup = useCallback(
    async (username: string, password: string) => {
      await endpoints.setup({ username, password });
      await login(username, password);
    },
    [login],
  );

  const logout = useCallback(async () => {
    try {
      await endpoints.logout();
    } catch {
      // The session may already be gone; the local state change below is what matters.
    }
    setState((s) => ({ ...s, status: 'anonymous', user: null }));
  }, []);

  const value = useMemo<AuthValue>(() => {
    const role = state.user?.role ?? null;
    return {
      ...state,
      role,
      can: (capability) => hasCapability(role, capability),
      refresh,
      login,
      setup,
      logout,
    };
  }, [state, refresh, login, setup, logout]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthValue {
  const value = useContext(AuthContext);
  if (!value) throw new Error('useAuth must be used inside <AuthProvider>');
  return value;
}
