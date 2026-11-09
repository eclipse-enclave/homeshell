import { useCallback, useEffect, useState } from 'react';

import type { DaemonErrorBody } from './field-errors';

export interface VersionInfo {
  /** Release channel the daemon updates from; empty when it never updates itself. */
  channel: string;
  /** Builds are versions on the stable channel and short commits on rolling. */
  current: string;
  latest: string;
  updateAvailable: boolean;
}

export type UpdatePhase = 'idle' | 'updating' | 'error';

export interface DaemonVersion {
  /** null until the first /api/version response arrives. */
  info: VersionInfo | null;
  phase: UpdatePhase;
  /** Why the last update failed, while `phase` is 'error'. */
  error: string | null;
  /** Triggers POST /api/update; the daemon restarts on success. The SSE hook
   *  recreates its EventSource on the resulting fatal error, re-authenticating
   *  with the rotated token so the stream comes back on the new version. */
  update: () => void;
}

/**
 * Tracks the daemon's version and drives the self-update trigger
 * (docs/ARCHITECTURE.md, "Self-update"). It fetches /api/version once connected and
 * re-fetches after every reconnect, which is exactly what happens once a
 * self-update restarts the daemon and the SSE hook recreates its EventSource
 * (see useDaemonState) to recover the stream on the new version.
 */
export function useDaemonVersion(
  connection: 'connecting' | 'connected' | 'offline',
): DaemonVersion {
  const [info, setInfo] = useState<VersionInfo | null>(null);
  const [phase, setPhase] = useState<UpdatePhase>('idle');
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (connection !== 'connected') return;

    const controller = new AbortController();
    fetch('/api/version', { signal: controller.signal })
      .then((res) => {
        if (!res.ok) {
          throw new Error(`daemon responded with ${res.status}`);
        }
        return res.json() as Promise<VersionInfo>;
      })
      .then(setInfo)
      .catch(() => {
        // The connection indicator already reports reachability.
      });
    return () => controller.abort();
  }, [connection]);

  const update = useCallback(() => {
    setPhase('updating');
    setError(null);
    requestUpdate().then((result) => {
      if (typeof result === 'string') {
        setError(result);
        setPhase('error');
        return;
      }
      if (result.updated) {
        // The daemon has swapped its binary and is restarting. Apply the
        // version from the response so the UI reflects the update
        // immediately — even when the SSE reconnect does not trigger a
        // visible connection state change (e.g. behind a proxy that
        // transparently recovers the backend connection).
        setInfo((prev) => ({
          channel: prev?.channel ?? '',
          current: result.version,
          latest: result.version,
          updateAvailable: false,
        }));
      }
      setPhase('idle');
    });
  }, []);

  return { info, phase, error, update };
}

/** POST /api/update; resolves to the daemon's answer or why it failed. */
async function requestUpdate(): Promise<
  { updated: boolean; version: string } | string
> {
  try {
    const res = await fetch('/api/update', { method: 'POST' });
    if (!res.ok) {
      const body = (await res
        .json()
        .catch(() => null)) as DaemonErrorBody | null;
      return body?.error ?? `daemon responded with ${res.status}`;
    }
    return (await res.json()) as { updated: boolean; version: string };
  } catch {
    return 'Could not reach the daemon.';
  }
}
