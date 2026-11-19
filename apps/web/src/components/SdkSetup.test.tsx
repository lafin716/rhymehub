import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { endpoints } from '../api/endpoints';
import type { SdkPrompt } from '../api/types';
import { SdkSetup } from './SdkSetup';

vi.mock('../api/endpoints', () => ({
  endpoints: { getSdkPrompt: vi.fn() },
}));

const getSdkPrompt = vi.mocked(endpoints.getSdkPrompt);

function prompt(extra: Partial<SdkPrompt> = {}): SdkPrompt {
  return {
    projectId: 'octobooks',
    serverUrl: 'http://localhost:3000',
    sdkVersion: '0.1.0',
    sdkAvailable: true,
    packages: [],
    prompt: 'Install the rhymehub SDK… VITE_RHYMEHUB_PROJECT=octobooks',
    skill: '---\nname: rhymehub-sdk\n---\nbody',
    skillPath: '.claude/skills/rhymehub-sdk/SKILL.md',
    ...extra,
  };
}

describe('SdkSetup', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders the prompt and skill for this origin and copies the prompt', async () => {
    getSdkPrompt.mockResolvedValue(prompt());
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
    render(<SdkSetup projectId="octobooks" />);

    expect(await screen.findByLabelText('Agent prompt')).toHaveProperty('value', prompt().prompt);
    expect(screen.getByLabelText('Claude Code skill')).toHaveProperty('value', prompt().skill);
    expect(screen.getByText('.claude/skills/rhymehub-sdk/SKILL.md')).toBeTruthy();
    expect(getSdkPrompt).toHaveBeenCalledWith('octobooks', window.location.origin);
    expect(screen.queryByRole('alert')).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Copy Agent prompt' }));
    expect(writeText).toHaveBeenCalledWith(prompt().prompt);
    expect(await screen.findByText('Copied')).toBeTruthy();
  });

  it('re-renders for another server URL and warns when tarballs are missing', async () => {
    getSdkPrompt.mockResolvedValue(prompt({ sdkAvailable: false }));
    const user = userEvent.setup();
    render(<SdkSetup projectId="octobooks" />);

    expect(await screen.findByRole('alert')).toBeTruthy();
    const input = screen.getByLabelText('rhymehub server URL');
    await user.clear(input);
    await user.type(input, 'http://rich.example.ts.net:7780');
    await user.click(screen.getByRole('button', { name: 'Update' }));
    await waitFor(() => expect(getSdkPrompt).toHaveBeenLastCalledWith('octobooks', 'http://rich.example.ts.net:7780'));
  });
});
