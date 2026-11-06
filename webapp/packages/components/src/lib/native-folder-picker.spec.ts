import { afterEach, describe, expect, it, vi } from 'vitest';
import { pickFolderNatively } from './native-folder-picker';

function stubFetch(response: Partial<Response> | Error) {
  const fetchMock = vi.fn((_url: string, _init: RequestInit) =>
    response instanceof Error
      ? Promise.reject(response)
      : Promise.resolve(response as Response),
  );
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function jsonResponse(status: number, body: unknown): Partial<Response> {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('pickFolderNatively', () => {
  it('returns the directory the user chose', async () => {
    const fetchMock = stubFetch(
      jsonResponse(200, { path: '/home/user/code/thing' }),
    );

    await expect(pickFolderNatively('/home/user')).resolves.toEqual({
      outcome: 'selected',
      path: '/home/user/code/thing',
    });

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/browse/native');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ path: '/home/user' });
  });

  it('sends an empty start path when none is given', async () => {
    const fetchMock = stubFetch(jsonResponse(200, { path: '/somewhere' }));

    await pickFolderNatively();

    const [, init] = fetchMock.mock.calls[0];
    expect(JSON.parse(init.body as string)).toEqual({ path: '' });
  });

  it('reports a dismissed dialog as cancelled, not unavailable', async () => {
    stubFetch(jsonResponse(200, { cancelled: true }));

    await expect(pickFolderNatively()).resolves.toEqual({
      outcome: 'cancelled',
    });
  });

  // Every non-answer collapses to `unavailable`, which is the caller's signal to
  // fall back to the in-app picker.
  it.each([
    ['no dialog tool on the host', jsonResponse(501, { error: 'no chooser' })],
    ['a public bind', jsonResponse(409, { error: 'not on this host' })],
    ['a dialog failure', jsonResponse(500, { error: 'display refused' })],
    ['a success with no path', jsonResponse(200, {})],
  ])('reports %s as unavailable', async (_name, response) => {
    stubFetch(response);

    await expect(pickFolderNatively()).resolves.toEqual({
      outcome: 'unavailable',
    });
  });

  it('reports an unreachable daemon as unavailable', async () => {
    stubFetch(new TypeError('Failed to fetch'));

    await expect(pickFolderNatively()).resolves.toEqual({
      outcome: 'unavailable',
    });
  });
});
