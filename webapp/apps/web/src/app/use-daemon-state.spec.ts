import { act, renderHook } from '@testing-library/react';
import type { NotificationMessage, Project } from '@homeshell/model';

import {
  FakeEventSource,
  latestEventSource as source,
} from '../testing/fake-event-source';
import { useDaemonState } from './use-daemon-state';
import { INITIAL_BACKOFF_MS } from './use-reconnecting-event-source';

function project(id: string): Project {
  return {
    id,
    name: id,
    path: `/tmp/${id}`,
    gitBacked: false,
    state: 'idle',
    runtimeState: 'stopped',
    effectiveState: 'idle',
    workstreams: [],
  } as unknown as Project;
}

describe('useDaemonState', () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });

  it('connects to the events endpoint', () => {
    renderHook(() => useDaemonState());
    expect(source().url).toBe('/api/events');
  });

  it('stores the latest snapshot and marks the connection live', () => {
    const { result } = renderHook(() => useDaemonState());
    expect(result.current.projects).toBeNull();
    expect(result.current.connection).toBe('connecting');
    act(() => {
      source().emit('state', { projects: [project('a'), project('b')] });
    });
    expect(result.current.connection).toBe('connected');
    expect(result.current.projects?.map((p) => p.id)).toEqual(['a', 'b']);
  });

  it('forwards notification events without storing them', () => {
    const received: NotificationMessage[] = [];
    renderHook(() => useDaemonState((m) => received.push(m)));
    act(() => {
      source().emit('notification', { id: 'n1', kind: 'waiting' });
    });
    expect(received.map((m) => m.id)).toEqual(['n1']);
  });

  it('reports offline on stream error', () => {
    const { result } = renderHook(() => useDaemonState());
    act(() => {
      source().onerror?.();
    });
    expect(result.current.connection).toBe('offline');
  });

  // The reconnect timing rules are covered once, in
  // use-reconnecting-event-source.spec.ts. What matters here is that this hook
  // resubscribes its own listeners to the replacement source, so a stream killed
  // by a token-rotating restart comes back with state rather than silent.
  it('resubscribes and recovers state after a fatal (CLOSED) error', () => {
    vi.useFakeTimers();
    try {
      const { result } = renderHook(() => useDaemonState());
      const first = source();
      act(() => first.fail(FakeEventSource.CLOSED));
      expect(result.current.connection).toBe('offline');

      act(() => vi.advanceTimersByTime(INITIAL_BACKOFF_MS));
      const second = source();
      expect(second).not.toBe(first);

      act(() => {
        second.open();
        second.emit('state', { projects: [project('a')] });
      });
      expect(result.current.connection).toBe('connected');
      expect(result.current.projects?.map((p) => p.id)).toEqual(['a']);
    } finally {
      vi.useRealTimers();
    }
  });
});
