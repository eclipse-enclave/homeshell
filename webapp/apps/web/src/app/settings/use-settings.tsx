import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from 'react';
import { DEFAULT_SETTINGS, mergeSettings, type Settings } from './settings-types';

/**
 * How long to wait after the last edit before writing to the daemon. Coalesces
 * a burst of edits (e.g. typing into a text field) into a single PUT instead of
 * one request per keystroke.
 */
const PERSIST_DEBOUNCE_MS = 300;

export interface SettingsContextValue {
  settings: Settings;
  /** Whether the initial load from the daemon has completed (succeeded or not). */
  loaded: boolean;
  /**
   * Whether settings loaded successfully from the daemon. Until this is true the
   * `settings` are just the code defaults, so editing is blocked: a full-document
   * PUT built from defaults would otherwise wipe whatever the daemon actually
   * holds. False after a failed load means "couldn't load; editing disabled".
   */
  ready: boolean;
  /**
   * The daemon's message from the last rejected write (e.g. a value that failed
   * validation), or null. Cleared when the next edit is made or a write
   * succeeds. The message begins with the offending field's dotted path.
   */
  saveError: string | null;
  /** Replace settings, or apply an updater function. */
  update: Dispatch<SetStateAction<Settings>>;
  /** Patch a single domain group. */
  setSection: <K extends keyof Settings>(
    section: K,
    patch: Partial<Settings[K]>
  ) => void;
  /** Reset one group, or all settings when no group is given. */
  reset: (section?: keyof Settings) => void;
}

const SettingsContext = createContext<SettingsContextValue | null>(null);

/**
 * A debounced, serialized writer for settings. Returns a `persist` function
 * that records the desired settings and schedules a write:
 *
 * - **Debounced** — rapid edits collapse into one PUT after a short quiet gap,
 *   so typing does not fire a request per keystroke.
 * - **Serialized** — at most one PUT is in flight; a newer edit made while a
 *   write is running is sent only once that write completes, so the daemon can
 *   never receive two writes out of order.
 * - **Rolled back** — if the daemon rejects a write (e.g. a value that fails
 *   validation) and no newer edit is queued, the UI is snapped back to the
 *   daemon's stored settings so the invalid value does not silently linger.
 *
 * `restore` is called with the daemon's current settings on a rejected write;
 * `onResult` reports the daemon's error message on a rejection (or null on a
 * successful write) so the UI can tell the user why a value did not stick.
 */
function usePersistSettings(
  restore: (settings: Settings) => void,
  onResult: (error: string | null) => void
): (settings: Settings) => void {
  // The most recently requested settings still waiting to be written.
  const pendingRef = useRef<Settings | null>(null);
  // Whether a PUT is currently in flight (the serialization guard).
  const inFlightRef = useRef(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const restoreRef = useRef(restore);
  restoreRef.current = restore;
  const onResultRef = useRef(onResult);
  onResultRef.current = onResult;

  const flush = useCallback(() => {
    // A write is already running; it will pick up `pendingRef` when it settles.
    if (inFlightRef.current) return;
    const next = pendingRef.current;
    if (next == null) return;
    pendingRef.current = null;
    inFlightRef.current = true;
    fetch('/api/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(next),
    })
      .then(async (res) => {
        if (res.ok) {
          onResultRef.current(null);
          // The daemon normalizes what it stores (trimming, assigning ids,
          // restoring the fixed row), so adopt its copy rather than leaving the
          // UI on the unnormalized draft until a reload. A newer edit wins.
          const body = await res.json().catch(() => null);
          if (isSettingsDocument(body) && pendingRef.current == null) {
            restoreRef.current(mergeSettings(body));
          }
          return;
        }
        // The daemon rejected the write. Surface why, then — if the user has
        // not moved on (no newer edit queued) — pull the stored settings back
        // into the UI so the rejected value does not stay visible as if saved.
        onResultRef.current(await readError(res));
        if (pendingRef.current != null) return;
        const stored = await fetch('/api/settings');
        const body = stored.ok ? await stored.json() : null;
        // The user may have edited again while we re-fetched; if so, that newer
        // value wins — don't clobber it with the daemon's older stored copy.
        if (body != null && pendingRef.current == null) {
          restoreRef.current(mergeSettings(body));
        }
      })
      .catch(() => {
        // A network failure is not a validation rejection and we can't know the
        // daemon's state, so we don't roll back — but we surface it rather than
        // silently keeping a phantom "saved" value (the next edit retries).
        onResultRef.current(
          'Could not reach the daemon — your change may not have been saved.'
        );
      })
      .finally(() => {
        inFlightRef.current = false;
        // Send whatever accumulated while this write was in flight.
        if (pendingRef.current != null) flush();
      });
  }, []);

  useEffect(
    () => () => {
      if (timerRef.current == null) return;
      clearTimeout(timerRef.current);
      // Do not drop a debounced-but-unsent write on teardown.
      flush();
    },
    [flush]
  );

  return useCallback(
    (settings: Settings) => {
      pendingRef.current = settings;
      if (timerRef.current != null) clearTimeout(timerRef.current);
      timerRef.current = setTimeout(flush, PERSIST_DEBOUNCE_MS);
    },
    [flush]
  );
}

/**
 * Whether a body is the settings document rather than some other JSON. Merging
 * a stray body over the defaults would silently reset every setting.
 */
function isSettingsDocument(body: unknown): boolean {
  return (
    typeof body === 'object' &&
    body !== null &&
    !Array.isArray(body) &&
    'projects' in body
  );
}

/** The daemon's `{error}` message from a rejected write, or a status fallback. */
async function readError(res: Response): Promise<string> {
  try {
    const body = await res.json();
    if (body && typeof body.error === 'string') return body.error;
  } catch {
    /* non-JSON body; fall through to the generic message */
  }
  return `The daemon rejected the settings (HTTP ${res.status}).`;
}

/**
 * Loads user settings from the daemon (GET /api/settings) and persists changes
 * back (PUT /api/settings) so they are shared across this daemon's clients.
 * Settings start at the code defaults and are replaced when the load resolves;
 * `loaded` reports when that has happened. A change made before the load
 * arrives wins — the load will not clobber it. Per-browser preferences (theme,
 * last-selected project) live in the browser, not here.
 */
export function SettingsProvider({ children }: { children: ReactNode }) {
  const [settings, setSettings] = useState<Settings>(DEFAULT_SETTINGS);
  const [loaded, setLoaded] = useState(false);
  const [ready, setReady] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  // Mirrors the current settings for computing the next value in mutations
  // without a functional updater (so persistence runs exactly once).
  const settingsRef = useRef(settings);
  settingsRef.current = settings;
  // Mirrors `ready` so commit() can gate on it without stale-closure risk.
  const readyRef = useRef(ready);
  readyRef.current = ready;
  // Set once the user changes anything, so a late initial load does not clobber
  // it.
  const changedRef = useRef(false);
  const persist = usePersistSettings(setSettings, setSaveError);

  useEffect(() => {
    let cancelled = false;
    fetch('/api/settings')
      .then(async (res) => {
        if (!res.ok) throw new Error(`load failed: HTTP ${res.status}`);
        return res.json();
      })
      .then((body) => {
        if (cancelled) return;
        if (body && !changedRef.current) setSettings(mergeSettings(body));
        // Only a successful load makes editing safe: without real data a PUT
        // would replace the daemon's whole document with the code defaults.
        setReady(true);
        setLoaded(true);
      })
      .catch(() => {
        if (!cancelled) setLoaded(true); // ready stays false → editing blocked
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const value = useMemo<SettingsContextValue>(() => {
    const commit = (next: Settings) => {
      // Never persist until settings loaded successfully (see `ready`).
      if (!readyRef.current) return;
      changedRef.current = true;
      settingsRef.current = next;
      // The new edit supersedes any prior rejection message.
      setSaveError(null);
      setSettings(next);
      persist(next);
    };
    return {
      settings,
      loaded,
      ready,
      saveError,
      update(action) {
        const next =
          typeof action === 'function'
            ? (action as (prev: Settings) => Settings)(settingsRef.current)
            : action;
        commit(next);
      },
      setSection(section, patch) {
        commit({
          ...settingsRef.current,
          [section]: { ...settingsRef.current[section], ...patch },
        } as Settings);
      },
      reset(section) {
        commit(
          section
            ? ({ ...settingsRef.current, [section]: DEFAULT_SETTINGS[section] } as Settings)
            : DEFAULT_SETTINGS
        );
      },
    };
  }, [settings, loaded, ready, saveError, persist]);

  return (
    <SettingsContext.Provider value={value}>
      {children}
    </SettingsContext.Provider>
  );
}

export function useSettings(): SettingsContextValue {
  const ctx = useContext(SettingsContext);
  if (!ctx) {
    throw new Error('useSettings must be used within a SettingsProvider');
  }
  return ctx;
}
