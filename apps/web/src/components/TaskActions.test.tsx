import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { endpoints } from '../api/endpoints';
import type { Role, Task, TaskState } from '../api/types';
import { TaskActions } from './TaskActions';

vi.mock('../api/endpoints', () => ({
  endpoints: {
    approveTask: vi.fn(),
    rejectTask: vi.fn(),
    retryTask: vi.fn(),
    cancelTask: vi.fn(),
    decidePermission: vi.fn(),
  },
}));

function task(state: TaskState, extra: Partial<Task> = {}): Task {
  return { id: 'task-1', state, ...extra } as unknown as Task;
}

function renderActions(state: TaskState, role: Role | null, extra: Partial<Task> = {}) {
  const onChanged = vi.fn();
  render(<TaskActions task={task(state, extra)} role={role} onChanged={onChanged} />);
  return { onChanged };
}

describe('TaskActions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('hides Approve from developers at REVIEW_READY', () => {
    renderActions('REVIEW_READY', 'developer');
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Reject' })).toBeNull();
  });

  it('shows Approve to maintainers at REVIEW_READY', () => {
    renderActions('REVIEW_READY', 'maintainer');
    expect(screen.getByRole('button', { name: 'Approve' })).not.toBeNull();
    expect(screen.getByRole('button', { name: 'Reject' })).not.toBeNull();
  });

  it('renders nothing when no action applies', () => {
    const { container } = render(<TaskActions task={task('COMPLETED')} role="admin" onChanged={vi.fn()} />);
    expect(container.innerHTML).toBe('');
  });

  it('sends approve with the release flag when the box is ticked', async () => {
    const user = userEvent.setup();
    const approved = task('APPROVED');
    vi.mocked(endpoints.approveTask).mockResolvedValue(approved);
    const { onChanged } = renderActions('REVIEW_READY', 'maintainer');

    await user.click(screen.getByLabelText('Also release'));
    await user.click(screen.getByRole('button', { name: 'Approve' }));

    expect(endpoints.approveTask).toHaveBeenCalledWith('task-1', true);
    expect(onChanged).toHaveBeenCalledWith(approved);
  });

  it('requires a reason before confirming a rejection', async () => {
    const user = userEvent.setup();
    renderActions('REVIEW_READY', 'maintainer');

    await user.click(screen.getByRole('button', { name: 'Reject' }));
    const confirm = screen.getByRole('button', { name: 'Confirm reject' }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);

    await user.type(screen.getByLabelText('Reason for rejecting'), 'Missing tests');
    expect(confirm.disabled).toBe(false);
  });

  it('offers Allow and Deny when a permission is pending', async () => {
    const user = userEvent.setup();
    vi.mocked(endpoints.decidePermission).mockResolvedValue(task('RUNNING'));
    renderActions('WAITING_APPROVAL', 'developer', {
      pendingApproval: [{ tool: 'Bash' }],
    } as Partial<Task>);

    expect(screen.getByText(/Bash/)).not.toBeNull();
    await user.click(screen.getByRole('button', { name: 'Deny' }));
    expect(endpoints.decidePermission).toHaveBeenCalledWith('task-1', false);
  });
});
