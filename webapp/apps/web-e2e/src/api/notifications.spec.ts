import { expect, test } from '@playwright/test';
import { setNotificationStyle } from '../notify-settings';
import { authHeader } from '../api-token';

/**
 * Black-box test of the activity-notification stream: notify-worthy session
 * transitions arrive as `notification` SSE events on GET /api/events.
 */
test.describe('activity notifications', () => {
  test('GET /api/events pushes waiting and finished notifications', async ({
    baseURL,
    request,
  }) => {
    test.setTimeout(60_000);

    await setNotificationStyle(request, 'in-app');
    // The daemon drops a subscriber that falls behind and expects it back, so
    // reconnecting on a closed stream is part of the contract under test.
    const deadline = Date.now() + 45_000;
    const seen: NotificationPayload[] = [];
    const pending = new Set(['waiting', 'finished']);
    while (pending.size > 0) {
      if (Date.now() > deadline) {
        throw new Error(
          `timed out; still missing kinds: ${[...pending].join(', ')}`,
        );
      }
      const controller = new AbortController();
      const res = await fetch(`${baseURL}/api/events`, {
        signal: controller.signal,
        headers: { accept: 'text/event-stream', ...authHeader() },
      });
      try {
        expect(res.status).toBe(200);
        await readNotifications(res.body!, pending, seen, deadline);
      } finally {
        controller.abort();
      }
    }

    const waiting = seen.find((n) => n.kind === 'waiting')!;
    expect(waiting.title).toContain('waiting for input');
    expect(waiting.sessionId).toBeTruthy();
    expect(waiting.workstreamId).toBeTruthy();
    expect(waiting.projectId).toBeTruthy();

    const finished = seen.find((n) => n.kind === 'finished')!;
    expect(finished.title).toContain('finished');
    expect(finished.sessionId).toBeTruthy();
  });
});

interface NotificationPayload {
  id: string;
  kind: string;
  title: string;
  body: string;
  projectId: string;
  workstreamId: string;
  sessionId: string;
}

/**
 * Reads one event stream, collecting `notification` events into `collected` and
 * clearing their kinds from `pending`. Returns on the last kind, the deadline,
 * or the stream ending.
 */
async function readNotifications(
  body: ReadableStream<Uint8Array>,
  pending: Set<string>,
  collected: NotificationPayload[],
  deadline: number,
): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  try {
    while (pending.size > 0 && Date.now() <= deadline) {
      const { value, done } = await reader.read();
      if (done) return;
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
          const note = JSON.parse(data) as NotificationPayload;
          collected.push(note);
          pending.delete(note.kind);
        }
      }
    }
  } finally {
    await reader.cancel().catch(() => undefined);
  }
}
