import { act, renderHook, waitFor } from '@testing-library/react';

import { parseLocation, useHashRoute } from './use-hash-route';

describe('parseLocation', () => {
  it('maps the settings hashbang to the settings route', () => {
    expect(parseLocation('#!/settings')).toEqual({ route: 'settings' });
  });

  it('extracts the settings category segment', () => {
    expect(parseLocation('#!/settings/workstreams')).toEqual({
      route: 'settings',
      category: 'workstreams',
    });
  });

  it('maps the logs hashbang to the logs route', () => {
    expect(parseLocation('#!/logs')).toEqual({ route: 'logs' });
  });

  it('treats the root, empty and unknown hashes as home', () => {
    expect(parseLocation('#!/')).toEqual({ route: 'home' });
    expect(parseLocation('')).toEqual({ route: 'home' });
    expect(parseLocation('#!/nope')).toEqual({ route: 'home' });
  });
});

describe('useHashRoute', () => {
  beforeEach(() => {
    window.location.hash = '';
  });

  it('reads the initial route and category from the hash', () => {
    window.location.hash = '#!/settings/providers';
    const { result } = renderHook(() => useHashRoute());
    expect(result.current.route).toBe('settings');
    expect(result.current.category).toBe('providers');
  });

  it('navigates to a settings category by updating the hash', () => {
    const { result } = renderHook(() => useHashRoute());
    expect(result.current.route).toBe('home');

    act(() => {
      result.current.navigate('settings', 'appearance');
    });
    expect(window.location.hash).toBe('#!/settings/appearance');
    expect(result.current.route).toBe('settings');
    expect(result.current.category).toBe('appearance');

    act(() => {
      result.current.navigate('home');
    });
    expect(result.current.route).toBe('home');
    expect(result.current.category).toBeUndefined();
  });

  it('navigates to the logs route', () => {
    const { result } = renderHook(() => useHashRoute());
    act(() => {
      result.current.navigate('logs');
    });
    expect(window.location.hash).toBe('#!/logs');
    expect(result.current.route).toBe('logs');
  });

  it('updates when the hash changes externally', async () => {
    const { result } = renderHook(() => useHashRoute());
    act(() => {
      window.location.hash = '#!/settings/providers';
    });
    // `hashchange` is dispatched asynchronously by the environment.
    await waitFor(() => expect(result.current.category).toBe('providers'));
    expect(result.current.route).toBe('settings');
  });
});
