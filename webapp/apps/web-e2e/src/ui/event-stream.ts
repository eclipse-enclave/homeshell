import { type Page } from '@playwright/test';

interface StreamScope {
  EventSource: typeof EventSource;
  hsStreams: EventSource[];
  hsOffline: boolean;
  hsHeld: boolean;
}

/**
 * Wraps the page's EventSource, before the app loads, so a spec can drop the
 * event stream and hold every reconnect off until it restores it, or show a
 * state of its own and keep the daemon's pushes from replacing it until it
 * lets them through again. A helper module, not a spec.
 */
export async function controlEventStream(page: Page) {
  await page.addInitScript(() => {
    const scope = globalThis as unknown as StreamScope;
    const Native = scope.EventSource;
    scope.hsStreams = [];
    scope.hsOffline = false;
    scope.hsHeld = false;
    scope.EventSource = class extends Native {
      constructor(url: string | URL, init?: EventSourceInit) {
        super(scope.hsOffline ? '/api/no-such-stream' : url, init);
        scope.hsStreams.push(this);
        const target = this as EventTarget;
        const add = target.addEventListener.bind(target);
        target.addEventListener = (type, listener, options) =>
          add(
            type,
            type === 'state' && typeof listener === 'function'
              ? (event) => {
                  if (!scope.hsHeld || 'hsHeld' in event) listener(event);
                }
              : listener,
            options,
          );
      }
    };
  });

  return {
    drop: () =>
      page.evaluate(() => {
        const scope = globalThis as unknown as StreamScope;
        scope.hsOffline = true;
        for (const source of scope.hsStreams) {
          source.close();
          source.dispatchEvent(new Event('error'));
        }
      }),
    restore: () =>
      page.evaluate(() => {
        (globalThis as unknown as StreamScope).hsOffline = false;
      }),
    /** Shows `state` (as GET /api/projects answers) until `release`. */
    hold: (state: unknown) =>
      page.evaluate((data) => {
        const scope = globalThis as unknown as StreamScope;
        scope.hsHeld = true;
        const event = Object.assign(new MessageEvent('state', { data }), {
          hsHeld: true,
        });
        scope.hsStreams
          .filter(
            (source) =>
              source.url.endsWith('/api/events') &&
              source.readyState !== EventSource.CLOSED,
          )
          .forEach((source) => source.dispatchEvent(event));
      }, JSON.stringify(state)),
    release: () =>
      page.evaluate(() => {
        (globalThis as unknown as StreamScope).hsHeld = false;
      }),
  };
}
