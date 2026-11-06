import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';

/** How long a focus target may take to show up before it is given up on. */
export const PENDING_FOCUS_MS = 10_000;

/**
 * Holds something to focus once it is on the page — a workstream the daemon is
 * still making, say. `settle` is tried after every render until it reports the
 * target done with (focused, or no longer wanted), and the target is dropped
 * after PENDING_FOCUS_MS regardless, so one that never shows up cannot take
 * focus later from wherever the user has gone. Returns the setter.
 */
export function usePendingFocus<T>(settle: (target: T) => boolean) {
  const pending = useRef<T | null>(null);
  // Bumped by each target set, which renders so that it is tried at once, and
  // restarts the clock on it.
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    if (generation === 0) return;
    const timer = setTimeout(() => {
      pending.current = null;
    }, PENDING_FOCUS_MS);
    return () => clearTimeout(timer);
  }, [generation]);

  useLayoutEffect(() => {
    if (pending.current !== null && settle(pending.current)) {
      pending.current = null;
    }
  });

  return useCallback((target: T) => {
    pending.current = target;
    setGeneration((current) => current + 1);
  }, []);
}
