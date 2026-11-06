import { expect, test } from '@playwright/test';

/**
 * Black-box tests for POST /api/browse/native — the daemon opening the *host
 * desktop's* own folder chooser for the project Browse button.
 *
 * **The dialog itself is deliberately never opened here.** A call that reaches
 * the chooser pops a real GTK/macOS/Windows folder dialog on whoever runs
 * `make test-e2e` and then blocks until a human answers it, which is the same
 * reason the host terminal and desktop-IDE open modes have no e2e. So this file
 * pins the contract around the dialog — that the route exists, on POST only —
 * and the outcomes are covered by unit tests
 * against an injected chooser (daemon/internal/api/browse_native_test.go) and by
 * the UI e2e, which stubs the endpoint.
 *
 * The in-app fallback this endpoint sits in front of — GET /api/browse — is
 * fully exercised in browse.spec.ts, and stays the path a networked client uses.
 */
test.describe('native browse API', () => {
  // A GET reaches the route without reaching the chooser, which is exactly what
  // makes it safe to assert on: 405 (not 404) proves the endpoint is registered
  // and that it is an action, not something a prefetch can trigger.
  test('GET /api/browse/native is not allowed', async ({ request }) => {
    const res = await request.get('/api/browse/native');
    expect(res.status()).toBe(405);
  });

  test('PUT /api/browse/native is not allowed', async ({ request }) => {
    const res = await request.put('/api/browse/native');
    expect(res.status()).toBe(405);
  });

  // The endpoint is behind the same token auth as the rest of /api: it opens a
  // window on the daemon host's screen, so an unauthenticated caller must not
  // be able to reach it at all.
  test('POST /api/browse/native requires authentication', async ({
    baseURL,
  }) => {
    const res = await fetch(`${baseURL}/api/browse/native`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: '' }),
    });
    expect(res.status).toBe(401);
  });
});
