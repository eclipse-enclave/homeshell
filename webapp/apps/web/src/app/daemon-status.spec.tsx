import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { vi } from 'vitest';

import { DaemonStatus } from './daemon-status';

describe('DaemonStatus update button', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  const stubVersion = (
    info: {
      channel?: string;
      current: string;
      latest: string;
      updateAvailable: boolean;
    },
    // 'unreachable' rejects the fetch; a missing body makes json() reject.
    updateResponse:
      { ok: boolean; status?: number; body?: unknown } | 'unreachable' = {
      ok: true,
      body: { updated: true, version: info.latest },
    },
  ) => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/api/version')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(info) });
      }
      if (url.endsWith('/api/update')) {
        if (updateResponse === 'unreachable') {
          return Promise.reject(new TypeError('Failed to fetch'));
        }
        const { ok, status = ok ? 200 : 500, body } = updateResponse;
        return Promise.resolve({
          ok,
          status,
          json: () =>
            body === undefined
              ? Promise.reject(new SyntaxError('Unexpected token'))
              : Promise.resolve(body),
        });
      }
      return Promise.reject(new Error(`unexpected fetch ${url}`));
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  };

  it('offers no update button when up to date', async () => {
    stubVersion({ current: '1.0.0', latest: '1.0.0', updateAvailable: false });
    render(<DaemonStatus connection="connected" />);
    await screen.findByText('daemon 1.0.0');
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('shows an update button and completes the update flow', async () => {
    const fetchMock = stubVersion({
      current: '1.0.0',
      latest: '1.1.0',
      updateAvailable: true,
    });
    render(<DaemonStatus connection="connected" />);

    const button = await screen.findByRole('button', {
      name: /update to 1\.1\.0/i,
    });

    await act(async () => {
      fireEvent.click(button);
    });

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith('/api/update', { method: 'POST' }),
    );
    // After a successful update the response carries the new version, so
    // the hook updates info immediately and the button disappears.
    await waitFor(() => expect(screen.queryByRole('button')).toBeNull());
    expect(screen.getByText('daemon 1.1.0')).toBeTruthy();
  });

  const failUpdate = async (
    response: Parameters<typeof stubVersion>[1],
  ): Promise<void> => {
    stubVersion(
      { current: '1.0.0', latest: '1.1.0', updateAvailable: true },
      response,
    );
    render(<DaemonStatus connection="connected" />);
    const button = await screen.findByRole('button', {
      name: /update to 1\.1\.0/i,
    });
    await act(async () => {
      fireEvent.click(button);
    });
  };

  it.each([
    {
      name: 'the daemon reason',
      response: {
        ok: false,
        body: { error: 'checksum mismatch for homeshell.tar.gz' },
      },
      reason: 'checksum mismatch for homeshell.tar.gz',
    },
    {
      name: 'the status for a non-JSON error',
      response: { ok: false, status: 502 },
      reason: 'daemon responded with 502',
    },
    {
      name: 'an unreachable daemon',
      response: 'unreachable' as const,
      reason: 'Could not reach the daemon.',
    },
  ])(
    'shows $name when an update fails and offers a retry',
    async ({ response, reason }) => {
      await failUpdate(response);

      const retry = await screen.findByRole('button', { name: /retry/i });
      const alert = screen.getByRole('alert');
      expect(alert.querySelector('p')?.textContent).toBe(reason);
      expect(retry.getAttribute('aria-describedby')).toBe(
        alert.querySelector('p')?.id,
      );
    },
  );

  it.each([409, 503])(
    'offers no retry when the daemon refuses the update with %i',
    async (status) => {
      const reason =
        'homeshell cannot replace /usr/local/bin/homeshell: its location is not writable';
      await failUpdate({ ok: false, status, body: { error: reason } });

      const toggle = await screen.findByRole('button', {
        name: 'Update needs manual steps',
      });
      expect(screen.queryByRole('button', { name: /retry/i })).toBeNull();
      expect(toggle.getAttribute('aria-expanded')).toBe('true');
      expect(screen.getByRole('alert').textContent).toContain(reason);
    },
  );

  it('closes the reason to its toggle and reopens it', async () => {
    await failUpdate({ ok: false, body: { error: 'checksum mismatch' } });
    const toggle = await screen.findByRole('button', {
      name: 'Update failure details',
    });

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(screen.queryByRole('alert')).toBeNull();
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).toBe(toggle);

    fireEvent.click(toggle);
    expect(screen.getByRole('alert').textContent).toContain(
      'checksum mismatch',
    );
  });

  it('copies the full reason', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    await failUpdate({ ok: false, body: { error: 'checksum mismatch' } });

    await act(async () => {
      fireEvent.click(await screen.findByRole('button', { name: 'Copy' }));
    });
    expect(writeText).toHaveBeenCalledWith('checksum mismatch');
    expect(screen.getByRole('button', { name: 'Copied' })).toBeTruthy();
  });

  it('names the rolling channel next to its commit, also after an update', async () => {
    stubVersion({
      channel: 'rolling',
      current: 'abc1234',
      latest: 'def5678',
      updateAvailable: true,
    });
    render(<DaemonStatus connection="connected" />);
    await screen.findByText('daemon abc1234 (rolling)');

    await act(async () => {
      fireEvent.click(
        await screen.findByRole('button', { name: /update to def5678/i }),
      );
    });
    await screen.findByText('daemon def5678 (rolling)');
  });

  it('shows a stable version without a channel', async () => {
    stubVersion({
      channel: 'stable',
      current: '1.0.0',
      latest: '1.0.0',
      updateAvailable: false,
    });
    render(<DaemonStatus connection="connected" />);
    await screen.findByText('daemon 1.0.0');
  });

  it('hides the update button while offline', async () => {
    stubVersion({ current: '1.0.0', latest: '1.1.0', updateAvailable: true });
    render(<DaemonStatus connection="offline" />);
    expect(screen.getByText('daemon offline')).toBeTruthy();
    // Even after the version fetch settles, no button appears while offline.
    await waitFor(() => expect(screen.queryByRole('button')).toBeNull());
  });
});
