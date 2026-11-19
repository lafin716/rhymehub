import type { ReactNode } from 'react';
import type { TaskEvent } from '../api/types';
import { payloadString, prettyJson } from '../lib/events';
import { formatDateTime } from '../lib/format';
import { stateLabel } from '../lib/states';
import type { TaskState } from '../api/types';

/** First non-empty string among the common text-bearing payload keys. */
function textOf(payload: Record<string, unknown>): string {
  for (const key of ['text', 'message', 'output', 'content', 'summary']) {
    const value = payloadString(payload, key);
    if (value) return value;
  }
  return '';
}

function Pre({ children, tone = 'default' }: { children: string; tone?: 'default' | 'error' }) {
  const style =
    tone === 'error'
      ? 'border-rose-200 bg-rose-50 text-rose-900 dark:border-rose-800 dark:bg-rose-950 dark:text-rose-100'
      : 'border-slate-200 bg-slate-50 text-slate-800 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-200';
  return (
    <pre className={`max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-md border p-2 font-mono text-xs ${style}`}>
      {children}
    </pre>
  );
}

function Bubble({ align, children }: { align: 'left' | 'right'; children: ReactNode }) {
  return (
    <div className={`flex ${align === 'right' ? 'justify-end' : 'justify-start'}`}>
      <div
        className={`max-w-[85%] whitespace-pre-wrap break-words rounded-lg px-3 py-2 text-sm ${
          align === 'right'
            ? 'bg-indigo-600 text-white'
            : 'bg-white text-slate-900 ring-1 ring-slate-200 dark:bg-slate-900 dark:text-slate-100 dark:ring-slate-700'
        }`}
      >
        {children}
      </div>
    </div>
  );
}

function Divider({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-center gap-2 text-xs text-slate-400 dark:text-slate-500" role="separator">
      <span className="h-px flex-1 bg-slate-200 dark:bg-slate-800" />
      <span>{children}</span>
      <span className="h-px flex-1 bg-slate-200 dark:bg-slate-800" />
    </div>
  );
}

function Notice({ tone, label, children }: { tone: 'accent' | 'success' | 'danger' | 'neutral'; label: string; children: ReactNode }) {
  const border = {
    accent: 'border-violet-300 dark:border-violet-700',
    success: 'border-emerald-300 dark:border-emerald-700',
    danger: 'border-rose-300 dark:border-rose-700',
    neutral: 'border-slate-300 dark:border-slate-700',
  }[tone];
  return (
    <div className={`rounded-md border-l-4 bg-white p-2 text-sm dark:bg-slate-900 ${border}`}>
      <div className="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400">{label}</div>
      {children}
    </div>
  );
}

function renderEvent(event: TaskEvent): ReactNode {
  const p = event.payload;
  switch (event.type) {
    case 'state_changed': {
      const to = payloadString(p, 'to');
      return <Divider>{to ? `→ ${stateLabel(to as TaskState)}` : 'State changed'}</Divider>;
    }
    case 'user_message':
      return <Bubble align="right">{textOf(p)}</Bubble>;
    case 'answer':
      return (
        <Bubble align="right">
          <span className="mb-1 block text-xs opacity-80">Answer</span>
          {textOf(p)}
        </Bubble>
      );
    case 'agent_text':
      return <Bubble align="left">{textOf(p)}</Bubble>;
    case 'question':
      return (
        <Notice tone="accent" label="Agent needs an answer">
          <span className="whitespace-pre-wrap">{textOf(p)}</span>
        </Notice>
      );
    case 'permission_request':
      return (
        <Notice tone="accent" label="Permission requested">
          <div className="font-mono text-xs">{payloadString(p, 'tool')}</div>
          {p.input !== undefined ? <Pre>{prettyJson(p.input)}</Pre> : null}
        </Notice>
      );
    case 'permission_decision': {
      const allowed = p.allow === true || payloadString(p, 'decision') === 'allow';
      return <Divider>{allowed ? 'Permission granted' : 'Permission denied'}</Divider>;
    }
    case 'tool_use':
      return (
        <Notice tone="neutral" label={`Tool · ${payloadString(p, 'name') || payloadString(p, 'tool') || 'call'}`}>
          {p.input !== undefined ? <Pre>{prettyJson(p.input)}</Pre> : null}
        </Notice>
      );
    case 'tool_result':
      return (
        <Notice tone="neutral" label="Tool result">
          <Pre>{textOf(p) || prettyJson(p)}</Pre>
        </Notice>
      );
    case 'agent_error':
      return (
        <Notice tone="danger" label="Agent error">
          <Pre tone="error">{textOf(p) || prettyJson(p)}</Pre>
        </Notice>
      );
    case 'test_result': {
      const status = payloadString(p, 'status') || 'unknown';
      const tone = status === 'passed' ? 'success' : status === 'failed' ? 'danger' : 'neutral';
      return (
        <Notice tone={tone} label={`Tests · ${status}`}>
          {textOf(p) ? <Pre>{textOf(p)}</Pre> : null}
        </Notice>
      );
    }
    case 'diff':
      return (
        <Notice tone="neutral" label="Diff">
          <span className="font-mono text-xs">{payloadString(p, 'stat') || 'Changes recorded'}</span>
        </Notice>
      );
    case 'usage': {
      const turns = payloadString(p, 'turns');
      const cost = payloadString(p, 'costUsd');
      return <Divider>{[turns && `${turns} turns`, cost && `$${cost}`].filter(Boolean).join(' · ') || 'Usage'}</Divider>;
    }
    default:
      return (
        <Notice tone="neutral" label={event.type}>
          {textOf(p) ? <span className="whitespace-pre-wrap">{textOf(p)}</span> : <Pre>{prettyJson(p)}</Pre>}
        </Notice>
      );
  }
}

export function EventTimeline({ events, emptyText = 'No activity yet.' }: { events: readonly TaskEvent[]; emptyText?: string }) {
  if (events.length === 0) {
    return <p className="py-6 text-center text-sm text-slate-500 dark:text-slate-400">{emptyText}</p>;
  }
  return (
    <ol className="flex flex-col gap-3" aria-label="Task timeline">
      {events.map((event) => (
        <li key={`${event.taskId}-${event.seq}`} data-seq={event.seq} data-type={event.type}>
          {renderEvent(event)}
          {event.type !== 'state_changed' && event.type !== 'usage' ? (
            <time className="mt-0.5 block text-right text-[11px] text-slate-400 dark:text-slate-500" dateTime={event.createdAt}>
              {formatDateTime(event.createdAt)}
            </time>
          ) : null}
        </li>
      ))}
    </ol>
  );
}
