import { expect, test } from '@playwright/test';
import { authHeader } from '../api-token';

/**
 * Black-box test of the test notification (`POST /api/notifications/test`): the
 * sample the settings page asks for when the user picks a delivery style, so
 * they can see what that style looks like before an agent ever needs them.
 *
 * The daemon owns delivery, which is the whole reason this endpoint exists
 * — the OS-native channel is a platform command no browser can run. Only the
 * in-app style is exercised here: it rides the SSE stream and never reaches the
 * OS, so the suite never pops a real desktop notification on a developer's
 * machine (the native routing is covered by the notify package's unit tests).
 *
 * Unlike the other notification specs this one writes **no settings at all** —
 * the style to demonstrate travels in the request — so it cannot disturb the
 * shared, process-global daemon the rest of the suite is reading.
 */
test.describe('test notification', () => {
  test('POST /api/notifications/test pushes a sample to the in-app channel', async ({
    baseURL,
    request,
  }) => {
    const controller = new AbortController();
    const stream = await fetch(`${baseURL}/api/events`, {
      signal: controller.signal,
      headers: { accept: 'text/event-stream', ...authHeader() },
    });
    try {
      expect(stream.status).toBe(200);

      const res = await request.post('/api/notifications/test', {
        data: { style: 'in-app' },
      });
      expect(res.status()).toBe(200);
      // The response says where it went, so a client never has to guess which
      // channels a style selects.
      expect(await res.json()).toEqual({
        style: 'in-app',
        channels: ['in-app'],
      });

      const sample = await readTestNotification(stream.body!, 15_000);
      expect(sample).not.toBeNull();
      // Daemon-authored text — the client renders it as-is.
      expect(sample!.title).toBeTruthy();
      expect(sample!.body).toBeTruthy();
      // No session transitioned, so the sample names none.
      expect(sample!.sessionId).toBe('');
    } finally {
      controller.abort();
    }
  });

  // Showing nothing is what "None" does, and the empty channel list is how the
  // daemon says so. That it truly pushed nothing is asserted in the notify
  // package's unit tests, not here: the hub broadcasts to every subscriber, so
  // "no sample reached my stream" is not provable while a concurrent spec may
  // be firing its own test through the same daemon.
  test('the silent style reports that it delivered nothing', async ({
    request,
  }) => {
    const res = await request.post('/api/notifications/test', {
      data: { style: 'none' },
    });
    expect(res.status()).toBe(200);
    expect(await res.json()).toEqual({ style: 'none', channels: [] });
  });

  test('an unknown style is refused', async ({ request }) => {
    const res = await request.post('/api/notifications/test', {
      data: { style: 'telepathy' },
    });
    expect(res.status()).toBe(400);
    expect((await res.json()).error).toContain('style');
  });
});

interface NotificationPayload {
  id: string;
  kind: string;
  title: string;
  body: string;
  sound?: string;
  sessionId: string;
}

/**
 * Reads a text/event-stream until a `notification` event of kind `test`
 * arrives, or the timeout lapses (which yields null — the demo cycler's own
 * waiting/finished notifications may pass by in the meantime and are ignored,
 * as may a test another spec fired: the hub broadcasts to every subscriber).
 */
async function readTestNotification(
  body: ReadableStream<Uint8Array>,
  timeoutMs: number,
): Promise<NotificationPayload | null> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  const deadline = Date.now() + timeoutMs;
  let buffer = '';
  try {
    while (Date.now() < deadline) {
      const read = await Promise.race([
        reader.read(),
        new Promise<null>((resolve) =>
          setTimeout(() => resolve(null), deadline - Date.now()),
        ),
      ]);
      if (read === null) return null;
      if (read.done) return null;
      buffer += decoder.decode(read.value, { stream: true });
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
        if (type !== 'notification') continue;
        const note = JSON.parse(data) as NotificationPayload;
        if (note.kind === 'test') return note;
      }
    }
    return null;
  } finally {
    await reader.cancel().catch(() => undefined);
  }
}
