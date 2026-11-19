import type { ReactNode } from 'react';
import { Card } from './ui';

/** Centered card used by the public Setup and Login screens. */
export function AuthCard({ title, subtitle, children }: { title: string; subtitle?: string; children: ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-50 p-4 dark:bg-slate-950">
      <div className="w-full max-w-sm">
        <div className="mb-6 text-center">
          <p className="text-2xl font-semibold tracking-tight text-slate-900 dark:text-slate-100">rhymehub</p>
          {subtitle ? <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">{subtitle}</p> : null}
        </div>
        <Card title={title}>{children}</Card>
      </div>
    </div>
  );
}
