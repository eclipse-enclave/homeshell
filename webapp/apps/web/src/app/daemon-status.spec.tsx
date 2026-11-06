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

  const stubVersion = (info: {
    current: string;
    latest: string;
    updateAvailable: boolean;
  }) => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/api/version')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(info) });
      }
      if (url.endsWith('/api/update')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ updated: true, version: info.latest }),
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

  it('hides the update button while offline', async () => {
    stubVersion({ current: '1.0.0', latest: '1.1.0', updateAvailable: true });
    render(<DaemonStatus connection="offline" />);
    expect(screen.getByText('daemon offline')).toBeTruthy();
    // Even after the version fetch settles, no button appears while offline.
    await waitFor(() => expect(screen.queryByRole('button')).toBeNull());
  });
});
