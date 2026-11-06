import { renderHook, waitFor } from '@testing-library/react';
import { vi } from 'vitest';

import { useFetchedJson } from './use-fetched-json';

function stubFetch(response: Partial<Response> | Error) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      if (response instanceof Error) throw response;
      return response as Response;
    }),
  );
}

function isNamed(body: unknown): body is { name: string } {
  return typeof (body as { name?: unknown } | null)?.name === 'string';
}

describe('useFetchedJson', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('returns the body the guard accepts', async () => {
    stubFetch({ ok: true, json: async () => ({ name: 'a' }) });
    const { result } = renderHook(() => useFetchedJson('/api/thing', isNamed));
    await waitFor(() =>
      expect(result.current).toEqual({ value: { name: 'a' }, failed: false }),
    );
    expect(fetch).toHaveBeenCalledWith('/api/thing');
  });

  it.each([
    ['a failed response', { ok: false, json: async () => ({ name: 'a' }) }],
    ['a body the guard refuses', { ok: true, json: async () => ({ id: 1 }) }],
    ['an unreachable daemon', new Error('offline')],
  ])('stays empty, and says it failed, on %s', async (_, response) => {
    stubFetch(response);
    const { result } = renderHook(() => useFetchedJson('/api/thing', isNamed));
    expect(result.current).toEqual({ value: null, failed: false });
    await waitFor(() =>
      expect(result.current).toEqual({ value: null, failed: true }),
    );
  });
});
