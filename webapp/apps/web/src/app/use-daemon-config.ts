import { useCallback, useEffect, useRef, useState } from 'react';

/** Daemon-reported effective runtime configuration (from `GET /api/config`). */
export interface DaemonConfig {
  /** The daemon's effective log level, e.g. "debug". */
  logLevel?: string;
}

export interface UseDaemonConfig {
  config: DaemonConfig | null;
  /**
   * Changes the daemon's log level at runtime (`PATCH /api/config`). Optimistic:
   * updates immediately and reverts on failure. Resolves to null on success or
   * an error message.
   */
  setLogLevel: (level: string) => Promise<string | null>;
}

/** Reads and mutates the daemon's effective configuration. */
export function useDaemonConfig(): UseDaemonConfig {
  const [config, setConfig] = useState<DaemonConfig | null>(null);
  const configRef = useRef<DaemonConfig | null>(config);
  configRef.current = config;

  // Config has no SSE push, so beyond the initial load we refetch when the
  // window regains focus. The log level is process-global daemon state, so a
  // change made elsewhere (another tab, or `homeshell` on the CLI) should show
  // here without a manual reload. Within one app the settings page and the log
  // pane share this hook instance, so they stay in sync instantly.
  useEffect(() => {
    let cancelled = false;
    const refresh = () => {
      fetch('/api/config')
        .then((res) => (res.ok ? res.json() : null))
        .then((body) => {
          if (!cancelled && body && typeof body === 'object') {
            setConfig(body as DaemonConfig);
          }
        })
        .catch(() => {
          /* leave config unchanged when the daemon is unreachable */
        });
    };
    refresh();
    window.addEventListener('focus', refresh);
    return () => {
      cancelled = true;
      window.removeEventListener('focus', refresh);
    };
  }, []);

  const setLogLevel = useCallback(async (level: string) => {
    const prev = configRef.current;
    setConfig({ ...(prev ?? {}), logLevel: level });
    try {
      const res = await fetch('/api/config', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ logLevel: level }),
      });
      if (!res.ok) {
        setConfig(prev);
        const body = await res.json().catch(() => null);
        return (body as { error?: string } | null)?.error ?? 'Failed to change the log level.';
      }
      const body = await res.json().catch(() => null);
      if (body && typeof body === 'object') setConfig(body as DaemonConfig);
      return null;
    } catch {
      setConfig(prev);
      return 'Could not reach the daemon.';
    }
  }, []);

  return { config, setLogLevel };
}
