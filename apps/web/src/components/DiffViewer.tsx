import { useMemo } from 'react';
import { parseUnifiedDiff, type DiffLineKind } from '../lib/diff';

const LINE_CLASS: Record<DiffLineKind, string> = {
  add: 'bg-emerald-50 text-emerald-900 dark:bg-emerald-950/60 dark:text-emerald-200',
  del: 'bg-rose-50 text-rose-900 dark:bg-rose-950/60 dark:text-rose-200',
  hunk: 'bg-sky-50 text-sky-700 dark:bg-sky-950/60 dark:text-sky-300',
  meta: 'text-slate-500 dark:text-slate-400',
  ctx: 'text-slate-800 dark:text-slate-200',
};

export function DiffViewer({ patch, stat }: { patch: string; stat?: string }) {
  const files = useMemo(() => parseUnifiedDiff(patch), [patch]);

  if (!patch.trim()) {
    return <p className="text-sm text-slate-500 dark:text-slate-400">No changes.</p>;
  }

  return (
    <div className="flex flex-col gap-3">
      {stat ? <pre className="whitespace-pre-wrap text-xs text-slate-500 dark:text-slate-400">{stat}</pre> : null}
      {files.map((file, index) => (
        <details key={`${file.path}-${index}`} open className="overflow-hidden rounded-md border border-slate-200 dark:border-slate-700">
          <summary className="flex cursor-pointer items-center justify-between gap-2 bg-slate-100 px-3 py-2 font-mono text-xs dark:bg-slate-800">
            <span className="truncate">{file.path}</span>
            <span className="shrink-0">
              <span className="text-emerald-700 dark:text-emerald-300">+{file.additions}</span>{' '}
              <span className="text-rose-700 dark:text-rose-300">-{file.deletions}</span>
            </span>
          </summary>
          <div className="max-h-[32rem] overflow-auto">
            <pre className="m-0 font-mono text-xs leading-5">
              {file.lines.map((line, i) => (
                <div key={i} className={`whitespace-pre px-3 ${LINE_CLASS[line.kind]}`}>
                  {line.text || ' '}
                </div>
              ))}
            </pre>
          </div>
        </details>
      ))}
    </div>
  );
}
