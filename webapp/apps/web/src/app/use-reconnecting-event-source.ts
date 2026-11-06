import { useEffect, useRef } from 'react';

/** Initial delay before recreating a dead EventSource, in milliseconds. */
export const INITIAL_BACKOFF_MS = 1000;
/** Upper bound for the reconnection backoff, in milliseconds. */
export const MAX_BACKOFF_MS = 30000;

export interface ReconnectingEventSourceHandlers {
  /**
   * Attaches the stream's event listeners to a freshly created EventSource.
   * Called again for every recreated source, so listeners must be registered
   * here rather than once by the caller.
   */
  subscribe: (source: EventSource) => void;
  /** Called when a source opens. */
  onOpen?: () => void;
  /** Called on every stream error, fatal or transient. */
  onError?: () => void;
}

/**
 * Subscribes to an SSE endpoint and keeps the subscription alive across fatal
 * errors. A native EventSource does not reconnect after a fatal error (e.g. the
 * 401 a token-rotating daemon restart returns until the fresh cookie is stored):
 * it goes to readyState CLOSED and stops. So on a CLOSED error this recreates the
 * source after an exponential backoff — the new request carries the re-issued
 * cookie and re-authenticates, recovering the stream across a daemon restart. A
 * CONNECTING error is a transient drop the native EventSource is already
 * retrying, so it is left alone to avoid double-reconnect storms.
 *
 * Both daemon streams (`/api/events`, `/api/logs`) go through this, so the
 * reconnect policy and its backoff live in one place instead of drifting apart.
 * This is transport resilience, not domain logic — see docs/ARCHITECTURE.md, "Principles".
 *
 * Scope: **the streams only.** One-shot `fetch` calls are not retried, and to a caller a
 * 401 is indistinguishable from a rejection — `use-settings` surfaces the daemon's
 * "unauthorized" under the offending field and rolls the edit back. The window is the
 * ~1s between the rotating restart and the healed reconnect, so a save landing inside it
 * is lost. Closing that needs a decision this hook cannot make: which writes are safe to
 * replay (`PUT /api/settings` is idempotent, `POST /api/projects` is not).
 */
export function useReconnectingEventSource(
  url: string,
  handlers: ReconnectingEventSourceHandlers,
): void {
  // Keep the latest handlers in a ref so the subscription effect depends only on
  // the url: re-running it whenever a caller's callback identity changes would
  // tear the stream down on every render.
  const handlersRef = useRef(handlers);
  handlersRef.current = handlers;

  useEffect(() => {
    let cancelled = false;
    let source: EventSource;
    let backoff = INITIAL_BACKOFF_MS;
    let retry: ReturnType<typeof setTimeout> | undefined;

    const connect = () => {
      if (cancelled) return;
      source = new EventSource(url);
      handlersRef.current.subscribe(source);
      source.onopen = () => {
        backoff = INITIAL_BACKOFF_MS;
        handlersRef.current.onOpen?.();
      };
      source.onerror = () => {
        handlersRef.current.onError?.();
        // Only a CLOSED source has given up; a CONNECTING one is still being
        // retried by the browser, so leave it be.
        if (cancelled || source.readyState !== EventSource.CLOSED) return;
        source.close();
        retry = setTimeout(connect, backoff);
        backoff = Math.min(backoff * 2, MAX_BACKOFF_MS);
      };
    };

    connect();
    return () => {
      cancelled = true;
      if (retry) clearTimeout(retry);
      source.close();
    };
  }, [url]);
}
