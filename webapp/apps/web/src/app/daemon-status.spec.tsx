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
    updateResponse: { ok: boolean; body: unknown } = {
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
        return Promise.resolve({
          ok: updateResponse.ok,
          json: () => Promise.resolve(updateResponse.body),
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

  it('names why an update failed and offers a retry', async () => {
    stubVersion(
      { current: '1.0.0', latest: '1.1.0', updateAvailable: true },
      { ok: false, body: { error: 'checksum mismatch for homeshell.tar.gz' } },
    );
    render(<DaemonStatus connection="connected" />);
    const button = await screen.findByRole('button', {
      name: /update to 1\.1\.0/i,
    });

    await act(async () => {
      fireEvent.click(button);
    });

    const retry = await screen.findByRole('button', { name: /retry/i });
    expect(retry.getAttribute('title')).toBe(
      'Update to 1.1.0 failed: checksum mismatch for homeshell.tar.gz',
    );
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
