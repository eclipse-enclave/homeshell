import { act, renderHook } from '@testing-library/react';

import {
  FakeEventSource,
  latestEventSource as source,
} from '../../testing/fake-event-source';
import { INITIAL_BACKOFF_MS } from '../use-reconnecting-event-source';
import { DEFAULT_MAX_RECORDS, useLogStream } from './use-log-stream';

describe('useLogStream', () => {
  /** Delivers one `log` event carrying the given record. */
  const emitLog = (record: unknown) => source().emit('log', record);

  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });

  it('connects to the log endpoint', () => {
    renderHook(() => useLogStream());
    expect(source().url).toBe('/api/logs');
  });

  it('appends log records in order and marks the connection live', () => {
    const { result } = renderHook(() => useLogStream());
    act(() => {
      emitLog({ seq: 1, time: 't1', level: 'info', msg: 'first' });
      emitLog({ seq: 2, time: 't2', level: 'warn', msg: 'second' });
    });
    expect(result.current.connection).toBe('connected');
    expect(result.current.records.map((r) => r.msg)).toEqual([
      'first',
      'second',
    ]);
  });

  it('caps retained records, dropping the oldest', () => {
    const { result } = renderHook(() => useLogStream());
    act(() => {
      for (let i = 0; i < DEFAULT_MAX_RECORDS + 5; i++) {
        emitLog({ seq: i, time: 't', level: 'info', msg: `m${i}` });
      }
    });
    expect(result.current.records).toHaveLength(DEFAULT_MAX_RECORDS);
    // The first five records were dropped; the newest is retained.
    expect(result.current.records[0].msg).toBe('m5');
    expect(result.current.records.at(-1)?.msg).toBe(`m${DEFAULT_MAX_RECORDS + 4}`);
  });

  it('honors a custom buffer size and re-caps when it shrinks', () => {
    const { result, rerender } = renderHook(({ max }) => useLogStream(max), {
      initialProps: { max: 5 },
    });
    act(() => {
      for (let i = 0; i < 5; i++) {
        emitLog({ seq: i, time: 't', level: 'info', msg: `m${i}` });
      }
    });
    expect(result.current.records).toHaveLength(5);
    expect(result.current.overflowed).toBe(false);

    // Shrinking the cap trims the buffer and flags overflow.
    rerender({ max: 3 });
    expect(result.current.records).toHaveLength(3);
    expect(result.current.records[0].msg).toBe('m2');
    expect(result.current.overflowed).toBe(true);
  });

  it('flags overflow once the ring buffer drops entries', () => {
    const { result } = renderHook(() => useLogStream());
    expect(result.current.overflowed).toBe(false);
    act(() => {
      for (let i = 0; i < DEFAULT_MAX_RECORDS; i++) {
        emitLog({ seq: i, time: 't', level: 'info', msg: `m${i}` });
      }
    });
    // Exactly at the cap, nothing has been dropped yet.
    expect(result.current.overflowed).toBe(false);
    act(() => {
      emitLog({ seq: DEFAULT_MAX_RECORDS, time: 't', level: 'info', msg: 'one-more' });
    });
    expect(result.current.overflowed).toBe(true);
  });

  it('skips a malformed frame without dropping later records', () => {
    const { result } = renderHook(() => useLogStream());
    act(() => {
      source().emitRaw('log', '{ not valid json');
      emitLog({ seq: 1, time: 't', level: 'info', msg: 'good' });
    });
    expect(result.current.records.map((r) => r.msg)).toEqual(['good']);
  });

  it('closes the EventSource on unmount', () => {
    const { unmount } = renderHook(() => useLogStream());
    const created = source();
    expect(created.closed).toBe(false);
    unmount();
    expect(created.closed).toBe(true);
  });

  it('reports offline on stream error', () => {
    const { result } = renderHook(() => useLogStream());
    act(() => {
      source().onerror?.();
    });
    expect(result.current.connection).toBe('offline');
  });

  it('marks the connection live as soon as the stream opens', () => {
    // Unlike the state stream there is no opening snapshot, so an open connection
    // is the only "live" signal until the daemon logs something.
    const { result } = renderHook(() => useLogStream());
    act(() => source().open());
    expect(result.current.connection).toBe('connected');
  });

  // The reconnect timing rules are covered once, in
  // use-reconnecting-event-source.spec.ts. What matters here is that this hook
  // resubscribes its own `log` listener to the replacement source, so a stream
  // killed by a token-rotating restart comes back with records rather than silent.
  it('resubscribes and keeps buffering after a fatal (CLOSED) error', () => {
    vi.useFakeTimers();
    try {
      const { result } = renderHook(() => useLogStream());
      const first = source();
      act(() => first.fail(FakeEventSource.CLOSED));
      expect(result.current.connection).toBe('offline');

      act(() => vi.advanceTimersByTime(INITIAL_BACKOFF_MS));
      const second = source();
      expect(second).not.toBe(first);

      act(() => {
        second.open();
        second.emit('log', { seq: 1, time: 't', level: 'info', msg: 'back' });
      });
      expect(result.current.connection).toBe('connected');
      expect(result.current.records.map((r) => r.msg)).toEqual(['back']);
    } finally {
      vi.useRealTimers();
    }
  });
});
