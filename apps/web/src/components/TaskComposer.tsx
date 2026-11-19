import { useState } from 'react';
import { endpoints } from '../api/endpoints';
import type { Role, Task } from '../api/types';
import { taskActions } from '../lib/states';
import { Button, ErrorMessage, Textarea } from './ui';

interface TaskComposerProps {
  task: Task;
  role: Role | null;
  onChanged: (task: Task) => void;
}

/**
 * Single input under the timeline. Sends an answer while the agent waits for input, and a
 * follow-up message when the task can take one. Otherwise it explains why it is disabled.
 */
export function TaskComposer({ task, role, onChanged }: TaskComposerProps) {
  const actions = taskActions(task.state, role);
  const mode: 'answer' | 'followUp' | null = actions.answer ? 'answer' : actions.followUp ? 'followUp' : null;
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  if (!mode) {
    return (
      <p className="text-sm text-slate-500 dark:text-slate-400">
        {actions.permission
          ? 'Respond to the permission request above to continue.'
          : 'The agent is working. You can send a follow-up once it is ready for review.'}
      </p>
    );
  }

  const submit = async () => {
    const value = text.trim();
    if (!value) return;
    setBusy(true);
    setError(null);
    try {
      const updated = mode === 'answer' ? await endpoints.answerTask(task.id, value) : await endpoints.sendTaskMessage(task.id, value);
      setText('');
      onChanged(updated);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <label htmlFor="task-composer" className="text-sm font-medium text-slate-700 dark:text-slate-200">
        {mode === 'answer' ? 'Answer the agent' : 'Send a follow-up'}
      </label>
      <Textarea
        id="task-composer"
        rows={3}
        value={text}
        placeholder={mode === 'answer' ? 'Your answer…' : 'What should change next?'}
        onChange={(e) => setText(e.target.value)}
      />
      <div>
        <Button type="submit" variant="primary" disabled={busy || !text.trim()}>
          {mode === 'answer' ? 'Send answer' : 'Send follow-up'}
        </Button>
      </div>
      <ErrorMessage error={error} />
    </form>
  );
}
