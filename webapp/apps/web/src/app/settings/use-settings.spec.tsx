import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';

import { DEFAULT_SETTINGS } from './settings-types';
import { SettingsProvider, useSettings } from './use-settings';

const wrapper = ({ children }: { children: ReactNode }) => (
  <SettingsProvider>{children}</SettingsProvider>
);

/** Stubs fetch: GET /api/settings returns getBody; PUT is recorded. */
function stubFetch(getBody: unknown) {
  const put = vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) });
  const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
    if (init?.method === 'PUT') return put(_url, init);
    return { ok: true, json: async () => getBody } as Response;
  });
  vi.stubGlobal('fetch', fetchMock);
  return { put };
}

describe('useSettings', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('starts at the defaults and marks loaded after fetching', async () => {
    stubFetch(null);
    const { result } = renderHook(() => useSettings(), { wrapper });
    expect(result.current.settings).toEqual(DEFAULT_SETTINGS);
    await waitFor(() => expect(result.current.loaded).toBe(true));
  });

  it('applies settings loaded from the daemon', async () => {
    stubFetch({ projects: { sidebarSort: 'name' } });
    const { result } = renderHook(() => useSettings(), { wrapper });
    await waitFor(() =>
      expect(result.current.settings.projects.sidebarSort).toBe('name')
    );
  });

  it('persists a section patch to the daemon', async () => {
    const { put } = stubFetch(null);
    const { result } = renderHook(() => useSettings(), { wrapper });
    await waitFor(() => expect(result.current.loaded).toBe(true));

    act(() => result.current.setSection('projects', { sidebarSort: 'name' }));
    expect(result.current.settings.projects.sidebarSort).toBe('name');
    await waitFor(() => expect(put).toHaveBeenCalled());
    const body = JSON.parse((put.mock.calls[0][1] as RequestInit).body as string);
    expect(body.projects.sidebarSort).toBe('name');
  });

  it('coalesces a burst of edits into a single write', async () => {
    const { put } = stubFetch(null);
    const { result } = renderHook(() => useSettings(), { wrapper });
    await waitFor(() => expect(result.current.loaded).toBe(true));

    act(() => {
      result.current.setSection('workstreams', { nameTemplate: 'a' });
      result.current.setSection('workstreams', { nameTemplate: 'ab' });
      result.current.setSection('workstreams', { nameTemplate: 'abc' });
    });
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const body = JSON.parse((put.mock.calls[0][1] as RequestInit).body as string);
    expect(body.workstreams.nameTemplate).toBe('abc');
  });

  it('rolls back to the daemon value when a write is rejected', async () => {
    // The daemon stores a valid value; the PUT of an invalid one is rejected.
    const stored = { providers: { refreshIntervalSec: 5 } };
    const put = vi.fn().mockResolvedValue({ ok: false, status: 400 });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        if (init?.method === 'PUT') return put(_url, init);
        return { ok: true, json: async () => stored } as Response;
      })
    );
    const { result } = renderHook(() => useSettings(), { wrapper });
    await waitFor(() =>
      expect(result.current.settings.providers.refreshIntervalSec).toBe(5)
    );

    act(() => result.current.setSection('providers', { refreshIntervalSec: 0 }));
    // Optimistically shown before the write settles.
    expect(result.current.settings.providers.refreshIntervalSec).toBe(0);
    // Snapped back to the daemon's stored value once the write is rejected.
    await waitFor(() =>
      expect(result.current.settings.providers.refreshIntervalSec).toBe(5)
    );
  });

  it('exposes the daemon error on a rejected write and clears it on the next edit', async () => {
    const message = 'providers.refreshIntervalSec must be at least 1, got 0';
    const stored = { providers: { refreshIntervalSec: 5 } };
    const put = vi
      .fn()
      .mockResolvedValue({ ok: false, status: 400, json: async () => ({ error: message }) });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        if (init?.method === 'PUT') return put(_url, init);
        return { ok: true, json: async () => stored } as Response;
      })
    );
    const { result } = renderHook(() => useSettings(), { wrapper });
    await waitFor(() => expect(result.current.loaded).toBe(true));

    act(() => result.current.setSection('providers', { refreshIntervalSec: 0 }));
    await waitFor(() => expect(result.current.saveError).toBe(message));

    // A fresh edit supersedes the stale rejection message.
    act(() => result.current.setSection('sessions', { previewLines: 12 }));
    expect(result.current.saveError).toBeNull();
  });

  it('resets a single section to its defaults', async () => {
    stubFetch(null);
    const { result } = renderHook(() => useSettings(), { wrapper });
    await waitFor(() => expect(result.current.loaded).toBe(true));

    act(() => result.current.setSection('sessions', { previewLines: 18 }));
    act(() => result.current.reset('sessions'));
    expect(result.current.settings.sessions.previewLines).toBe(
      DEFAULT_SETTINGS.sessions.previewLines
    );
  });

  it('blocks editing and persistence when the initial load fails', async () => {
    const put = vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        if (init?.method === 'PUT') return put(_url, init);
        return { ok: false, status: 503 } as Response; // initial GET fails
      })
    );
    const { result } = renderHook(() => useSettings(), { wrapper });
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(result.current.ready).toBe(false);

    act(() => result.current.setSection('projects', { sidebarSort: 'name' }));
    // The edit is dropped: with no real data, a full-document PUT would replace
    // the daemon's settings with the code defaults. State stays unchanged...
    expect(result.current.settings.projects.sidebarSort).toBe('insertion');
    // ...and nothing is written even after the debounce window.
    await new Promise((resolve) => setTimeout(resolve, 350));
    expect(put).not.toHaveBeenCalled();
  });

  it('surfaces a network failure on save', async () => {
    const put = vi.fn().mockRejectedValue(new Error('network down'));
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        if (init?.method === 'PUT') return put(_url, init);
        return { ok: true, json: async () => null } as Response;
      })
    );
    const { result } = renderHook(() => useSettings(), { wrapper });
    await waitFor(() => expect(result.current.ready).toBe(true));

    act(() => result.current.setSection('projects', { sidebarSort: 'name' }));
    await waitFor(() =>
      expect(result.current.saveError).toMatch(/reach the daemon/)
    );
  });

  it('throws when used outside a provider', () => {
    expect(() => renderHook(() => useSettings())).toThrow(/SettingsProvider/);
  });
});
