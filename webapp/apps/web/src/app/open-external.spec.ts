import { afterEach, describe, expect, it, vi } from 'vitest';
import { openExternal } from './open-external';

const URL_UNDER_TEST = 'http://127.0.0.1:9999/?folder=/work';

function stubWindowOpen(result: Window | null) {
  const open = vi.fn(() => result);
  vi.stubGlobal('open', open);
  return open;
}

function stubFetch(response: Partial<Response> | Error) {
  const fetchMock = vi.fn((_url: string, _init: RequestInit) =>
    response instanceof Error
      ? Promise.reject(response)
      : Promise.resolve(response as Response),
  );
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('openExternal', () => {
  it('opens a window directly when the client allows it', async () => {
    const handle = {} as Window;
    const open = stubWindowOpen(handle);
    const fetchMock = stubFetch({ ok: true } as Partial<Response>);

    await expect(openExternal(URL_UNDER_TEST)).resolves.toBeNull();

    // No `noopener` in the features, and that is the point rather than an
    // omission: with it, the spec has window.open return null even on success, so
    // a successful open is indistinguishable from a blocked one and the daemon
    // fallback fires on top of it — the same URL opened twice on every click.
    expect(open).toHaveBeenCalledWith(URL_UNDER_TEST, '_blank');
    // What `noopener` was doing is done here instead, so the opened page cannot
    // reach back into this one.
    expect(handle.opener).toBeNull();
    // No point troubling the daemon when the window already opened.
    expect(fetchMock).not.toHaveBeenCalled();
  });

  // This is the desktop shell: its webview returns null from window.open every
  // time, which used to mean clicking a web-IDE mode did nothing at all. It is
  // also a browser's popup blocker, which fires because the URL is only known
  // after awaiting the daemon.
  it('asks the daemon to open it when window.open is blocked', async () => {
    stubWindowOpen(null);
    const fetchMock = stubFetch({ ok: true } as Partial<Response>);

    await expect(openExternal(URL_UNDER_TEST)).resolves.toBeNull();

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/open-url');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ url: URL_UNDER_TEST });
  });

  it("surfaces the daemon's reason when the hand-off fails", async () => {
    stubWindowOpen(null);
    stubFetch({
      ok: false,
      status: 409,
      json: () => Promise.resolve({ error: 'no desktop opener on this host' }),
    } as Partial<Response>);

    await expect(openExternal(URL_UNDER_TEST)).resolves.toBe(
      'no desktop opener on this host',
    );
  });

  it('falls back to a generic message when the daemon gives no reason', async () => {
    stubWindowOpen(null);
    stubFetch({
      ok: false,
      status: 500,
      json: () => Promise.reject(new Error('not JSON')),
    } as Partial<Response>);

    await expect(openExternal(URL_UNDER_TEST)).resolves.toContain(
      'Could not open',
    );
  });

  it('reports an unreachable daemon', async () => {
    stubWindowOpen(null);
    stubFetch(new TypeError('Failed to fetch'));

    await expect(openExternal(URL_UNDER_TEST)).resolves.toBe(
      'Could not reach the daemon.',
    );
  });
});
