import { useRef, useState } from 'react';
import type { NotificationMessage, Project } from '@homeshell/model';

import { useReconnectingEventSource } from './use-reconnecting-event-source';

export type DaemonConnection = 'connecting' | 'connected' | 'offline';

export interface DaemonState {
  /** null until the first snapshot arrives. */
  projects: readonly Project[] | null;
  connection: DaemonConnection;
}

interface StatePayload {
  projects: Project[];
}

/**
 * Subscribes to the daemon's SSE stream (GET /api/events, see
 * docs/ARCHITECTURE.md, "Pushing state to clients"). The stream opens with a
 * full-state snapshot and pushes a full-state event on every change, so this hook holds no
 * domain logic — it stores whatever the daemon sent last. Heartbeat events
 * arrive while nothing changes, and each reconnect starts with a fresh snapshot.
 *
 * Reconnection after a fatal stream error (e.g. the 401 a token-rotating daemon
 * restart returns) is handled by useReconnectingEventSource.
 *
 * The same stream also multiplexes ephemeral `notification` events (issue #66);
 * when an `onNotification` callback is given, each one is forwarded to it. They
 * are fire-once events, not state, so they are never stored or replayed.
 */
export function useDaemonState(
  onNotification?: (message: NotificationMessage) => void,
): DaemonState {
  const [projects, setProjects] = useState<readonly Project[] | null>(null);
  const [connection, setConnection] = useState<DaemonConnection>('connecting');
  // Keep the latest callback in a ref so the subscription effect stays stable
  // (it must not re-open the stream when the callback identity changes).
  const notifyRef = useRef(onNotification);
  notifyRef.current = onNotification;

  const onState = (event: Event) => {
    const payload = JSON.parse(
      (event as MessageEvent<string>).data,
    ) as StatePayload;
    setProjects(payload.projects);
    setConnection('connected');
  };
  const onHeartbeat = () => setConnection('connected');
  const onNotify = (event: Event) => {
    const message = JSON.parse(
      (event as MessageEvent<string>).data,
    ) as NotificationMessage;
    notifyRef.current?.(message);
  };

  useReconnectingEventSource('/api/events', {
    subscribe: (source) => {
      source.addEventListener('state', onState);
      source.addEventListener('heartbeat', onHeartbeat);
      source.addEventListener('notification', onNotify);
    },
    // Not 'connected' on open: the stream counts as live once its opening
    // snapshot arrives, which onState reports.
    onError: () => setConnection('offline'),
  });

  return { projects, connection };
}
