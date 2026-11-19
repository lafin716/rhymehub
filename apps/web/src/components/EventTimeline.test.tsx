import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { TaskEvent } from '../api/types';
import { EventTimeline } from './EventTimeline';

function ev(seq: number, type: string, payload: Record<string, unknown> = {}): TaskEvent {
  return {
    taskId: 't1',
    seq,
    type,
    payload,
    createdAt: '2026-01-01T00:00:00Z',
  } as unknown as TaskEvent;
}

describe('EventTimeline', () => {
  it('shows the empty text when there are no events', () => {
    render(<EventTimeline events={[]} emptyText="Nothing here." />);
    expect(screen.getByText('Nothing here.')).not.toBeNull();
  });

  it('renders one item per event with seq and type attributes, in the order given', () => {
    const { container } = render(
      <EventTimeline
        events={[
          ev(1, 'user_message', { text: 'Fix the login bug' }),
          ev(2, 'agent_text', { text: 'On it.' }),
          ev(3, 'question', { text: 'Which branch?' }),
        ]}
      />,
    );

    const items = container.querySelectorAll('li[data-seq]');
    expect([...items].map((li) => li.getAttribute('data-seq'))).toEqual(['1', '2', '3']);
    expect([...items].map((li) => li.getAttribute('data-type'))).toEqual(['user_message', 'agent_text', 'question']);
    expect(screen.getByText('Fix the login bug')).not.toBeNull();
    expect(screen.getByText('Which branch?')).not.toBeNull();
    expect(screen.getByText('Agent needs an answer')).not.toBeNull();
  });

  it('shows test status and falls back to raw JSON for unknown event types', () => {
    render(
      <EventTimeline
        events={[ev(1, 'test_result', { status: 'failed', text: '2 failing' }), ev(2, 'mystery', { foo: 'bar' })]}
      />,
    );

    expect(screen.getByText('Tests · failed')).not.toBeNull();
    expect(screen.getByText('2 failing')).not.toBeNull();
    expect(screen.getByText('mystery')).not.toBeNull();
    expect(screen.getByText(/"foo": "bar"/)).not.toBeNull();
  });
});
