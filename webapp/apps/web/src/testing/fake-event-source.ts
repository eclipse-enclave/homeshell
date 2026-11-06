type Listener = (event: { data: string }) => void;

/**
 * jsdom has no EventSource; this fake lets tests drive an SSE stream — emit
 * events, fire `onopen`, and fail with a chosen `readyState` so the reconnect
 * logic in `useReconnectingEventSource` can be exercised. Shared by every spec
 * that needs a stream: `FakeEventSource.reset()` then
 * `vi.stubGlobal('EventSource', FakeEventSource)` in `beforeEach`, and read the
 * current instance with `latestEventSource()` (or `eventSourceForUrl()` when the
 * component under test opens more than one).
 *
 * `readyState` and the static `CLOSED` are what make it a faithful stand-in:
 * `useReconnectingEventSource` decides fatal-vs-transient by comparing them, and a
 * hand-rolled fake missing both turns that comparison into `undefined !== undefined`,
 * so every error silently takes the fatal path.
 *
 * Deliberately free of vitest imports: this file lives under `src`, so the app's
 * own typecheck compiles it and test globals are not in scope there.
 */
export class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  static latest: FakeEventSource | null = null;
  /** Every instance ever constructed, in creation order. */
  static instances: FakeEventSource[] = [];

  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  /** Mirrors EventSource.readyState; onerror inspects it to decide recovery. */
  readyState = FakeEventSource.CONNECTING;
  private listeners = new Map<string, Listener[]>();

  constructor(readonly url: string) {
    FakeEventSource.latest = this;
    FakeEventSource.instances.push(this);
  }

  /** Clears the shared bookkeeping between tests. */
  static reset() {
    FakeEventSource.latest = null;
    FakeEventSource.instances = [];
  }

  addEventListener(type: string, listener: Listener) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  close() {
    this.closed = true;
    this.readyState = FakeEventSource.CLOSED;
  }

  /** Fire onopen as the browser would once the connection is established. */
  open() {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.();
  }

  /** Fire onerror with the readyState the browser would have set. */
  fail(readyState: number) {
    this.readyState = readyState;
    this.onerror?.();
  }

  /** Deliver a named event carrying `data` as its JSON payload. */
  emit(type: string, data: unknown) {
    this.emitRaw(type, JSON.stringify(data));
  }

  /** Deliver a named event with a verbatim payload (for malformed-frame cases). */
  emitRaw(type: string, data: string) {
    for (const listener of this.listeners.get(type) ?? []) {
      listener({ data });
    }
  }
}

/** The most recently constructed fake source; throws if nothing opened a stream. */
export function latestEventSource(): FakeEventSource {
  if (!FakeEventSource.latest) throw new Error('no EventSource created');
  return FakeEventSource.latest;
}

/**
 * The **most recent** fake source whose URL contains `fragment`. The full app opens two
 * streams (`/api/events` and `/api/logs`), so a spec rendering it has to pick the one
 * under test by URL rather than by "the most recent overall".
 *
 * Most recent *for that URL*, not first: `useReconnectingEventSource` recreates the
 * source on a fatal error with the same URL, so returning the first would hand back the
 * closed original. A spec driving the reconnect path would then emit on a dead instance
 * and still pass — the old listeners are live closures — while proving nothing.
 */
export function eventSourceForUrl(fragment: string): FakeEventSource {
  const matching = FakeEventSource.instances.filter((source) =>
    source.url.includes(fragment),
  );
  const instance = matching.at(-1);
  if (!instance) throw new Error(`no EventSource for ${fragment}`);
  return instance;
}
