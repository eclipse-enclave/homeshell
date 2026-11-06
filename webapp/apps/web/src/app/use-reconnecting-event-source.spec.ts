import { act, renderHook } from '@testing-library/react';

import {
  FakeEventSource,
  latestEventSource as source,
} from '../testing/fake-event-source';
import {
  INITIAL_BACKOFF_MS,
  MAX_BACKOFF_MS,
  useReconnectingEventSource,
} from './use-reconnecting-event-source';

/** Records every source the hook subscribed, as the fake type the tests drive. */
function recorder(into: FakeEventSource[]) {
  return (created: EventSource) => {
    into.push(created as unknown as FakeEventSource);
  };
}

/**
 * The reconnect policy shared by both daemon SSE streams: a fatal (CLOSED) error
 * recreates the source after an exponential backoff, a transient (CONNECTING)
 * error is left to the browser's own retry. The consuming hooks
 * (`use-daemon-state`, `use-log-stream`) only assert that they recover — the
 * timing rules live here.
 */
describe('useReconnectingEventSource', () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });

  it('connects to the given url and subscribes the caller', () => {
    const subscribed: FakeEventSource[] = [];
    renderHook(() =>
      useReconnectingEventSource('/api/events', { subscribe: recorder(subscribed) }),
    );
    expect(source().url).toBe('/api/events');
    expect(subscribed).toEqual([source()]);
  });

  it('reports open and error to the caller', () => {
    const events: string[] = [];
    renderHook(() =>
      useReconnectingEventSource('/api/events', {
        subscribe: () => undefined,
        onOpen: () => events.push('open'),
        onError: () => events.push('error'),
      }),
    );
    act(() => source().open());
    act(() => source().fail(FakeEventSource.CONNECTING));
    expect(events).toEqual(['open', 'error']);
  });

  it('recreates the EventSource after a fatal (CLOSED) error and resubscribes', () => {
    vi.useFakeTimers();
    try {
      const subscribed: FakeEventSource[] = [];
      renderHook(() =>
        useReconnectingEventSource('/api/events', { subscribe: recorder(subscribed) }),
      );
      const first = source();
      act(() => first.fail(FakeEventSource.CLOSED));
      expect(first.closed).toBe(true);
      expect(FakeEventSource.instances).toHaveLength(1);

      act(() => vi.advanceTimersByTime(INITIAL_BACKOFF_MS));
      expect(FakeEventSource.instances).toHaveLength(2);
      expect(source()).not.toBe(first);
      // Listeners are attached to the replacement too, or the stream would come
      // back silent.
      expect(subscribed).toEqual([first, source()]);
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not recreate the EventSource on a transient (CONNECTING) error', () => {
    vi.useFakeTimers();
    try {
      renderHook(() =>
        useReconnectingEventSource('/api/events', { subscribe: () => undefined }),
      );
      const first = source();
      act(() => first.fail(FakeEventSource.CONNECTING));
      act(() => vi.advanceTimersByTime(60000));
      // The native EventSource is still retrying; we must not pile on.
      expect(FakeEventSource.instances).toHaveLength(1);
      expect(first.closed).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it('grows the backoff on repeated errors and resets after reconnect', () => {
    vi.useFakeTimers();
    try {
      renderHook(() =>
        useReconnectingEventSource('/api/events', { subscribe: () => undefined }),
      );
      // First failure retries after 1s.
      act(() => source().fail(FakeEventSource.CLOSED));
      act(() => vi.advanceTimersByTime(999));
      expect(FakeEventSource.instances).toHaveLength(1);
      act(() => vi.advanceTimersByTime(1));
      expect(FakeEventSource.instances).toHaveLength(2);

      // Second failure backs off to 2s.
      act(() => source().fail(FakeEventSource.CLOSED));
      act(() => vi.advanceTimersByTime(1999));
      expect(FakeEventSource.instances).toHaveLength(2);
      act(() => vi.advanceTimersByTime(1));
      expect(FakeEventSource.instances).toHaveLength(3);

      // A successful open resets the backoff to the initial delay.
      act(() => source().open());
      act(() => source().fail(FakeEventSource.CLOSED));
      act(() => vi.advanceTimersByTime(INITIAL_BACKOFF_MS));
      expect(FakeEventSource.instances).toHaveLength(4);
    } finally {
      vi.useRealTimers();
    }
  });

  it('caps the backoff at 30s', () => {
    vi.useFakeTimers();
    try {
      renderHook(() =>
        useReconnectingEventSource('/api/events', { subscribe: () => undefined }),
      );
      // Doubling from the initial delay reaches the ceiling, which then holds for every
      // further failure instead of growing past it. Derived from the exported constants
      // rather than hardcoded, so changing either cannot leave this asserting the old
      // schedule.
      const delays = [
        INITIAL_BACKOFF_MS,
        INITIAL_BACKOFF_MS * 2,
        INITIAL_BACKOFF_MS * 4,
        INITIAL_BACKOFF_MS * 8,
        INITIAL_BACKOFF_MS * 16,
        MAX_BACKOFF_MS,
        MAX_BACKOFF_MS,
      ];
      delays.forEach((delay, attempt) => {
        act(() => source().fail(FakeEventSource.CLOSED));
        act(() => vi.advanceTimersByTime(delay - 1));
        expect(FakeEventSource.instances).toHaveLength(attempt + 1);
        act(() => vi.advanceTimersByTime(1));
        expect(FakeEventSource.instances).toHaveLength(attempt + 2);
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it('closes the EventSource on unmount', () => {
    const { unmount } = renderHook(() =>
      useReconnectingEventSource('/api/events', { subscribe: () => undefined }),
    );
    const created = source();
    expect(created.closed).toBe(false);
    unmount();
    expect(created.closed).toBe(true);
  });

  it('cancels a pending reconnect on unmount', () => {
    vi.useFakeTimers();
    try {
      const { unmount } = renderHook(() =>
        useReconnectingEventSource('/api/events', { subscribe: () => undefined }),
      );
      act(() => source().fail(FakeEventSource.CLOSED));
      unmount();
      act(() => vi.advanceTimersByTime(60000));
      // The retry timer was cleared, so no new source appears.
      expect(FakeEventSource.instances).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not tear the stream down when a handler identity changes', () => {
    const { rerender } = renderHook(
      ({ tag }: { tag: string }) =>
        useReconnectingEventSource('/api/events', {
          subscribe: () => undefined,
          onOpen: () => void tag,
        }),
      { initialProps: { tag: 'a' } },
    );
    const first = source();
    rerender({ tag: 'b' });
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(first.closed).toBe(false);
  });
});
