import { expect, test } from '@playwright/test';
import { authHeader } from '../api-token';

/**
 * Black-box test for the log-stream endpoint. It streams Server-Sent Events, so
 * it is read incrementally with the Node fetch reader rather than Playwright's
 * buffering request fixture. A 4xx request produces a Warn-level "api request"
 * record, which streams at the daemon's default level without changing it (so
 * this test does not race the config-API tests on the process-global level).
 *
 * The raw fetch targets the project `baseURL` (the same daemon the `request`
 * fixture uses) rather than a private constant, so it can't drift from the
 * configured daemon port.
 */
test('GET /api/logs streams log records as they are emitted', async ({
  request,
  baseURL,
}) => {
  const controller = new AbortController();
  try {
    const res = await fetch(new URL('/api/logs', baseURL), {
      signal: controller.signal,
      headers: { ...authHeader() },
    });
    expect(res.status).toBe(200);
    expect(res.headers.get('content-type')).toContain('text/event-stream');
    expect(res.body).not.toBeNull();

    const reader = res.body!.getReader();
    const decoder = new TextDecoder();

    // Generate a logged request: an unknown /api route returns 404, logged at
    // Warn, which reaches the stream at the daemon's default level.
    const bad = await request.get('/api/__no_such_route__');
    expect(bad.status()).toBe(404);

    let buffer = '';
    const deadline = Date.now() + 15_000;
    while (Date.now() < deadline && !buffer.includes('event: log')) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
    }
    expect(buffer).toContain('event: log');
    expect(buffer).toContain('api request');
  } finally {
    controller.abort();
  }
});
