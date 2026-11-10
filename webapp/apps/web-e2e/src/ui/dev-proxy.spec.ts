import { expect, test } from '../fixtures';

/**
 * The dev/e2e vite proxy speaks for the browser: on a proxied /api request it strips
 * the dev-server `Origin` and injects the API token as a Bearer header, which is what
 * makes the browser -> vite -> daemon path authenticate in dev (vite.config.mts).
 *
 * Doing that for every request would make the proxy a confused deputy — any page in
 * the world could reach the daemon through it as a trusted local client — so it only
 * vouches for requests that came from a page this dev server served. These cases pin
 * that gate: it is dev-only wiring, so it has no api-project equivalent, and without
 * a test the `if (!sameOriginAsDevServer(req)) return` could be dropped unnoticed.
 *
 * The requests go through Playwright's request context rather than the page, because
 * the point is to control `Origin` directly (a browser will not send a forged one).
 */
test.describe('dev proxy origin gate', () => {
  test('a foreign Origin is neither stripped nor vouched for', async ({
    request,
  }) => {
    const res = await request.get('/api/hello', {
      headers: { Origin: 'http://evil.example' },
    });
    // Forwarded untouched, so the daemon's own loopback guard refuses it — before
    // authentication, hence 403 rather than the 401 a missing token would give.
    expect(res.status()).toBe(403);
  });

  // The body has to be one the daemon would accept, or a regression shows up as the
  // handler's 400 and the write is never actually attempted.
  test('a foreign Origin cannot write through the proxy', async ({ request }) => {
    const res = await request.post('/api/projects', {
      headers: { Origin: 'http://evil.example' },
      data: { path: '/tmp' },
    });
    expect(res.status()).toBe(403);

    // And nothing was created: the request must not reach the handler at all.
    const projects = await request.get('/api/projects');
    expect(projects.status()).toBe(200);
    const body = (await projects.json()) as { projects?: { path?: string }[] };
    expect(body.projects?.some((project) => project.path === '/tmp')).toBeFalsy();
  });

  test('a request from this dev server is proxied with the token', async ({
    request,
    baseURL,
  }) => {
    if (!baseURL) throw new Error('the ui project always configures a baseURL');
    const res = await request.get('/api/hello', { headers: { Origin: baseURL } });
    expect(res.status()).toBe(200);
  });
});
