import { act, renderHook } from '@testing-library/react';
import { useNow } from './use-now';

describe('useNow', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('moves on once per interval', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-11-10T12:00:00Z'));
    const { result } = renderHook(() => useNow(60_000));
    expect(result.current.toISOString()).toBe('2026-11-10T12:00:00.000Z');
    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(result.current.toISOString()).toBe('2026-11-10T12:01:00.000Z');
  });

  it('shares one timer per interval', () => {
    vi.useFakeTimers();
    const first = renderHook(() => useNow(60_000));
    const second = renderHook(() => useNow(60_000));
    expect(vi.getTimerCount()).toBe(1);
    first.unmount();
    second.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
