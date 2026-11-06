import { useEffect, useRef, useState } from 'react';
import type { LogRecord } from '@homeshell/model';

import type { DaemonConnection } from '../use-daemon-state';
import { useReconnectingEventSource } from '../use-reconnecting-event-source';

export interface LogStream {
  /** Records received so far, oldest first, capped at the buffer size. */
  records: readonly LogRecord[];
  connection: DaemonConnection;
  /**
   * True once the ring buffer has overflowed and the oldest records have been
   * dropped — the viewer surfaces a placeholder so it is clear that earlier
   * entries existed but are no longer shown.
   */
  overflowed: boolean;
}

/** Default log ring-buffer size (entries), used when no preference is stored. */
export const DEFAULT_MAX_RECORDS = 1000;
/** Smallest log ring-buffer size a user may configure. */
export const MIN_RECORDS = 100;

/**
 * Subscribes to the daemon's log stream (GET /api/logs). The stream sends no
 * snapshot: records arrive only for what the daemon logs from the moment the
 * connection opens, so the buffer fills as activity happens. What arrives
 * follows the daemon's live log level.
 *
 * Reconnection after a fatal stream error (e.g. the 401 a token-rotating daemon
 * restart returns) is handled by useReconnectingEventSource.
 *
 * `maxRecords` bounds the ring buffer; the oldest entries are dropped past it.
 * It can change at runtime (from the user's setting) and the buffer re-caps.
 */
export function useLogStream(maxRecords: number = DEFAULT_MAX_RECORDS): LogStream {
  const [records, setRecords] = useState<readonly LogRecord[]>([]);
  const [connection, setConnection] = useState<DaemonConnection>('connecting');
  const [overflowed, setOverflowed] = useState(false);
  // Total records received this session. Once it passes the current cap the
  // buffer has dropped its oldest entries (whether trimmed on append or after
  // the cap shrank). Tracked in a ref so detection stays out of the updater.
  const receivedRef = useRef(0);
  const maxRef = useRef(maxRecords);
  maxRef.current = maxRecords;

  const onLog = (event: Event) => {
    let record: LogRecord;
    try {
      record = JSON.parse((event as MessageEvent<string>).data) as LogRecord;
    } catch {
      // A malformed frame must not tear down the listener; skip it.
      return;
    }
    setConnection('connected');
    receivedRef.current += 1;
    if (receivedRef.current > maxRef.current) setOverflowed(true);
    setRecords((prev) => {
      const next = [...prev, record];
      return next.length > maxRef.current
        ? next.slice(next.length - maxRef.current)
        : next;
    });
  };
  const onHeartbeat = () => setConnection('connected');

  useReconnectingEventSource('/api/logs', {
    subscribe: (source) => {
      source.addEventListener('log', onLog);
      source.addEventListener('heartbeat', onHeartbeat);
    },
    // Unlike the state stream this one sends no opening snapshot, so an open
    // connection is the only "live" signal there is until a record arrives.
    onOpen: () => setConnection('connected'),
    onError: () => setConnection('offline'),
  });

  // Re-cap when the buffer size shrinks below what is currently held.
  useEffect(() => {
    if (receivedRef.current > maxRecords) setOverflowed(true);
    setRecords((prev) =>
      prev.length > maxRecords ? prev.slice(prev.length - maxRecords) : prev
    );
  }, [maxRecords]);

  return { records, connection, overflowed };
}
