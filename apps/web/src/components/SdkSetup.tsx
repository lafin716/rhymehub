import { useState, type FormEvent, type ReactNode } from 'react';
import { endpoints } from '../api/endpoints';
import { useAsync } from '../state/useApi';
import { Badge, Button, Card, ErrorMessage, Field, Input, Spinner, Textarea } from './ui';

/**
 * Shows the agent prompt (and the same text as a Claude Code skill) that installs the rhymehub SDK and feedback
 * widget into the project's app. The text never contains a key; the user creates one in "SDK keys".
 */
export function SdkSetup({ projectId }: { projectId: string }) {
  const [draft, setDraft] = useState(() => (typeof window === 'undefined' ? '' : window.location.origin));
  const [serverUrl, setServerUrl] = useState(draft);
  const prompt = useAsync(() => endpoints.getSdkPrompt(projectId, serverUrl || undefined), [projectId, serverUrl]);

  function apply(e: FormEvent) {
    e.preventDefault();
    setServerUrl(draft.trim());
  }

  const d = prompt.data;
  return (
    <div className="flex flex-col gap-4">
      <Card title="Add the SDK and feedback widget to your app">
        <ol className="mb-3 list-decimal space-y-1 pl-5 text-sm text-slate-700 dark:text-slate-300">
          <li>Copy the prompt below and paste it into Claude Code (or another coding agent) running in your app repository.
            Or save the skill version at the shown path and ask the agent to “add rhymehub”.</li>
          <li>The agent vendors the SDK tarballs from this server, mounts the widget and adds a <code>.env.example</code>.</li>
          <li>Create a key in the <strong>SDK keys</strong> tab and put it in the app's <code>.env.local</code> yourself.
            The prompt never contains a key.</li>
        </ol>
        <form onSubmit={apply} className="flex flex-wrap items-end gap-2">
          <div className="min-w-[16rem] flex-1">
            <Field id="sdk-server-url" label="rhymehub server URL" hint="The address your app's dev server can reach (localhost, LAN or Tailscale).">
              <Input id="sdk-server-url" value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="http://127.0.0.1:7780" />
            </Field>
          </div>
          <Button type="submit" variant="primary">Update</Button>
        </form>
      </Card>

      <ErrorMessage error={prompt.error} />
      {prompt.loading && !d ? <Spinner label="Rendering prompt…" /> : null}
      {d ? (
        <>
          {!d.sdkAvailable ? (
            <p role="alert" className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
              This rhymehub build has no embedded SDK tarballs, so the download links will return 404. Rebuild with
              <code> make build</code>.
            </p>
          ) : null}
          <CopyBlock
            id="sdk-prompt"
            title="Agent prompt"
            meta={<Badge tone="info">SDK {d.sdkVersion}</Badge>}
            text={d.prompt}
          />
          <CopyBlock id="sdk-skill" title="Claude Code skill" meta={<code className="text-xs">{d.skillPath}</code>} text={d.skill} />
        </>
      ) : null}
    </div>
  );
}

function CopyBlock({ id, title, meta, text }: { id: string; title: string; meta: ReactNode; text: string }) {
  const [copied, setCopied] = useState<'idle' | 'done' | 'failed'>('idle');

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied('done');
    } catch {
      // Clipboard needs a secure context (https or localhost); fall back to selecting the text.
      const el = document.getElementById(id) as HTMLTextAreaElement | null;
      el?.select();
      setCopied('failed');
    }
  }

  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          {title} {meta}
        </span>
      }
      actions={
        <Button onClick={() => void copy()} aria-label={`Copy ${title}`}>
          {copied === 'done' ? 'Copied' : copied === 'failed' ? 'Selected — press ⌘C' : 'Copy'}
        </Button>
      }
    >
      <Textarea id={id} aria-label={title} readOnly rows={14} value={text} />
    </Card>
  );
}
