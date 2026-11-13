import { useCallback, useState, useSyncExternalStore } from 'react';

interface Ticker {
  now: Date;
  listeners: Set<() => void>;
  timer: ReturnType<typeof setInterval>;
}

/** One timer per interval, shared by every component on it. */
const tickers = new Map<number, Ticker>();

function subscribe(intervalMs: number, listener: () => void): () => void {
  let ticker = tickers.get(intervalMs);
  if (!ticker) {
    const listeners = new Set<() => void>();
    const started: Ticker = {
      now: new Date(),
      listeners,
      timer: setInterval(() => {
        started.now = new Date();
        for (const notify of listeners) notify();
      }, intervalMs),
    };
    ticker = started;
    tickers.set(intervalMs, ticker);
  }
  const { listeners, timer } = ticker;
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) {
      clearInterval(timer);
      tickers.delete(intervalMs);
    }
  };
}

/**
 * The current time, moving on every `intervalMs`, for text such as "12 min
 * ago" that goes stale as it sits on screen.
 */
export function useNow(intervalMs: number): Date {
  const [mounted] = useState(() => new Date());
  const subscribeTo = useCallback(
    (listener: () => void) => subscribe(intervalMs, listener),
    [intervalMs],
  );
  return useSyncExternalStore(
    subscribeTo,
    () => tickers.get(intervalMs)?.now ?? mounted,
  );
}
