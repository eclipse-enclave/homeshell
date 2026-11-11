import { expect, test } from '@playwright/test';
import { spawn } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { daemonDir, homeshellBin as binary } from '../e2e-env';
import {
  isolatedHome,
  runtimeEnv,
  trackIsolatedHome,
} from '../isolated-daemon';

/**
 * Black-box coverage of the per-event notification settings: which notify-worthy
 * events alert, and which of those are audible. The daemon owns both decisions
 * (whether to push at all, and the sound the client plays), so this asserts the
 * public contract and would survive a daemon rewrite.
 *
 * Unlike the other notification specs, this one runs **its own daemon** on an
 * ephemeral port with an isolated home directory (the `daemon-lifecycle` spec's
 * pattern). Turning an event off is exactly what the shared, process-global
 * daemon must never have done to it mid-run — a spec reading that channel would
 * hang — so the interference is removed rather than tiptoed around. The style is
 * `in-app` throughout, which rides the SSE stream and never reaches the OS.
 *
 * The demo cycler drives a session running → waiting → idle every cycle, so both
 * kinds arrive without any action.
 */

const homeDir = isolatedHome('ntf');
const stateFile = join(homeDir, 'daemon.json');

/** The sound the seeded settings choose, so a stamped sound is unambiguous. */
const SOUND = 'chime';
/** The daemon's "no sound" name: delivered, but silent on every channel. */
const SILENT = 'none';

interface DaemonState {
  address: string;
  token: string;
}

interface NotificationPayload {
  id: string;
  kind: string;
  title: string;
  sound?: string;
  sessionId: string;
}

let daemon: ReturnType<typeof spawn> | undefined;
let state: DaemonState;

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function waitFor(predicate: () => boolean, timeoutMs: number) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline && !predicate()) await sleep(50);
  return predicate();
}

const authHeaders = () => ({ Authorization: `Bearer ${state.token}` });

/**
 * GET-modify-PUT of the isolated daemon's settings document, returning the
 * notifications group the daemon echoed back.
 */
async function patchNotifications(patch: Record<string, unknown>) {
  const current = (await (
    await fetch(`http://${state.address}/api/settings`, {
      headers: authHeaders(),
    })
  ).json()) as { notifications: Record<string, unknown> };
  const res = await fetch(`http://${state.address}/api/settings`, {
    method: 'PUT',
    headers: { ...authHeaders(), 'content-type': 'application/json' },
    body: JSON.stringify({
      ...current,
      notifications: { ...current.notifications, ...patch },
    }),
  });
  expect(res.status).toBe(200);
  const echoed = (await res.json()) as {
    notifications: Record<string, unknown>;
  };
  return echoed.notifications;
}

/**
 * Reads the notification stream for `durationMs`, returning everything seen. It
 * reads for the full duration unless `until` says it has seen enough, so a
 * caller can assert that a kind never arrived. `onOpen` runs once the stream is
 * subscribed, so whatever it triggers is not missed.
 */
async function collectNotifications(
  durationMs: number,
  {
    onOpen,
    until,
  }: {
    onOpen?: () => Promise<void>;
    until?: (seen: NotificationPayload[]) => boolean;
  } = {},
): Promise<NotificationPayload[]> {
  const controller = new AbortController();
  const res = await fetch(`http://${state.address}/api/events`, {
    signal: controller.signal,
    headers: { accept: 'text/event-stream', ...authHeaders() },
  });
  expect(res.status).toBe(200);
  const timer = setTimeout(() => controller.abort(), durationMs);
  const reader = res.body!.getReader();
  const decoder = new TextDecoder();
  const collected: NotificationPayload[] = [];
  let buffer = '';
  try {
    await onOpen?.();
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let separator: number;
      while ((separator = buffer.indexOf('\n\n')) >= 0) {
        const block = buffer.slice(0, separator);
        buffer = buffer.slice(separator + 2);
        let type = 'message';
        let data = '';
        for (const line of block.split('\n')) {
          if (line.startsWith('event: ')) type = line.slice(7);
          else if (line.startsWith('data: ')) data += line.slice(6);
        }
        if (type === 'notification') {
          collected.push(JSON.parse(data) as NotificationPayload);
        }
      }
      if (until?.(collected)) break;
    }
  } catch (error) {
    // The abort at the deadline is the normal way out of this loop.
    if (!controller.signal.aborted) throw error;
  } finally {
    clearTimeout(timer);
    await reader.cancel().catch(() => undefined);
  }
  return collected;
}

test.describe('per-event notification settings', () => {
  // The tests build one daemon and change its settings in turn, so they run in
  // order in one worker.
  test.describe.configure({ mode: 'default' });

  test.beforeAll(async () => {
    test.setTimeout(90_000);
    // Seed the settings before the daemon starts: in-app only (never the OS),
    // "waiting" audible, "finished" muted, and no quiet period — the cycler's
    // sessions are short-lived, and a window here would only add flakiness.
    writeFileSync(
      join(homeDir, 'settings.json'),
      JSON.stringify({
        schemaVersion: 1,
        settings: {
          notifications: {
            style: 'in-app',
            sound: SOUND,
            waiting: { notify: true, sound: true },
            finished: { notify: true, sound: false },
            quietSeconds: 0,
          },
        },
      }),
    );
    daemon = spawn(
      binary,
      ['serve', '-demo=true', `-home-dir=${homeDir}`, '-addr', '127.0.0.1:0'],
      {
        cwd: daemonDir,
        stdio: 'ignore',
        env: { ...process.env, ...runtimeEnv(homeDir) },
      },
    );
    expect(await waitFor(() => existsSync(stateFile), 60_000)).toBe(true);
    state = JSON.parse(readFileSync(stateFile, 'utf8')) as DaemonState;
  });

  test.afterAll(trackIsolatedHome(homeDir, () => daemon));

  test('stamps the configured sound per event, and silences a muted one', async () => {
    test.setTimeout(120_000);
    // Two cycles' worth, so both kinds are seen even if the stream opens
    // mid-cycle.
    const seen = await collectNotifications(40_000);

    const waiting = seen.filter((n) => n.kind === 'waiting');
    const finished = seen.filter((n) => n.kind === 'finished');
    expect(waiting.length).toBeGreaterThan(0);
    expect(finished.length).toBeGreaterThan(0);

    // The audible event carries the chosen sound; the muted one is delivered
    // all the same, stamped silent — "tell me, but don't make a noise".
    for (const note of waiting) expect(note.sound).toBe(SOUND);
    for (const note of finished) expect(note.sound).toBe(SILENT);
  });

  test('an event turned off stops arriving while the other keeps coming', async () => {
    test.setTimeout(120_000);
    const stored = await patchNotifications({
      finished: { notify: false, sound: false },
    });
    // The switches survive the round-trip, so what the settings UI sends is what
    // the Monitor reads back.
    expect(stored.finished).toEqual({ notify: false, sound: false });
    expect(stored.waiting).toEqual({ notify: true, sound: true });

    const seen = await collectNotifications(40_000);
    // The stream is still live and the cycler still running…
    expect(seen.filter((n) => n.kind === 'waiting').length).toBeGreaterThan(0);
    // …but the event that was turned off is not pushed on any channel.
    expect(seen.filter((n) => n.kind === 'finished')).toEqual([]);
  });

  // The sample sent when a style is picked plays the sound only while an
  // alerting event is audible.
  for (const { name, events, want } of [
    {
      name: 'the test notification plays the sound while an event is audible',
      events: {
        waiting: { notify: true, sound: true },
        finished: { notify: true, sound: false },
      },
      want: SOUND,
    },
    {
      name: 'the test notification is silent once every event is muted',
      events: {
        waiting: { notify: true, sound: false },
        finished: { notify: true, sound: false },
      },
      want: SILENT,
    },
  ]) {
    test(name, async () => {
      await patchNotifications(events);
      const isSample = (n: NotificationPayload) => n.kind === 'test';
      const seen = await collectNotifications(10_000, {
        onOpen: async () => {
          const res = await fetch(
            `http://${state.address}/api/notifications/test`,
            {
              method: 'POST',
              headers: {
                ...authHeaders(),
                'content-type': 'application/json',
              },
              body: JSON.stringify({ style: 'in-app' }),
            },
          );
          expect(res.status).toBe(200);
        },
        until: (collected) => collected.some(isSample),
      });
      const samples = seen.filter(isSample);
      expect(samples).toHaveLength(1);
      expect(samples[0].sound).toBe(want);
    });
  }
});
