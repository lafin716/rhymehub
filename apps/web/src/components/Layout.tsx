import { NavLink, Outlet } from 'react-router';
import { useAuth } from '../state/AuthContext';
import { ROLE_LABEL, type Capability } from '../lib/roles';
import { ConnectionBanner } from './ConnectionBanner';
import { Button } from './ui';

// `capability` hides a nav entry for roles that cannot use the screen at all.
// Screens still gate their own controls; the server enforces the real rules.
const NAV: { to: string; label: string; end?: boolean; capability?: Capability }[] = [
  { to: '/', label: 'Dashboard', end: true },
  { to: '/projects', label: 'Projects' },
  { to: '/inbox', label: 'Inbox', capability: 'manageRequests' },
  { to: '/agents', label: 'Agents' },
  { to: '/releases', label: 'Releases' },
  { to: '/notifications', label: 'Notifications' },
  { to: '/settings', label: 'Settings' },
];

export function Layout() {
  const { user, can, logout } = useAuth();

  return (
    <div className="min-h-screen bg-slate-50 text-slate-900 dark:bg-slate-950 dark:text-slate-100">
      <ConnectionBanner />
      <header className="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-4 px-4 py-3">
          <span className="font-semibold tracking-tight">rhymehub</span>
          <nav aria-label="Main" className="flex flex-wrap gap-1">
            {NAV.filter((item) => !item.capability || can(item.capability)).map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.end}
                className={({ isActive }) =>
                  `rounded-md px-2.5 py-1.5 text-sm ${
                    isActive
                      ? 'bg-indigo-50 font-medium text-indigo-700 dark:bg-indigo-950 dark:text-indigo-200'
                      : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'
                  }`
                }
              >
                {item.label}
              </NavLink>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-3 text-sm">
            {user ? (
              <span className="text-slate-600 dark:text-slate-300">
                {user.username} <span className="text-xs text-slate-400">({ROLE_LABEL[user.role]})</span>
              </span>
            ) : null}
            <Button variant="ghost" onClick={() => void logout()}>
              Sign out
            </Button>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-6xl px-4 py-6">
        <Outlet />
      </main>
    </div>
  );
}
