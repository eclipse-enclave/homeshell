import { expect, test } from '@playwright/test';

/**
 * Black-box tests for POST /api/open-url — the daemon's hand-off of a URL to the
 * host's default browser, which the UI falls back to whenever `window.open` is
 * blocked (always in the desktop shell's webview, and in a browser whenever the
 * URL is only known after an await).
 *
 * **Only the refusals are tested.** A successful call opens a real browser
 * window on whoever runs `make test-e2e`, for the same reason the host terminal
 * and desktop-IDE open modes have no e2e. The success path
 * is covered by unit tests that inject a recording opener.
 *
 * The refusals are the half worth pinning here anyway: the scheme allow-list is
 * the endpoint's entire security control. The platform helpers behind it
 * ("xdg-open", "open", "start") will open a file manager for a file:// URL and
 * launch whatever application has registered a custom scheme, so a leak here
 * turns one authenticated request into arbitrary local program launches.
 */
test.describe('open-url API', () => {
  const REFUSED: Array<{ name: string; url: string }> = [
    { name: 'a local file', url: 'file:///etc/passwd' },
    { name: 'a script URL', url: 'javascript:alert(1)' },
    { name: 'a registered application scheme', url: 'vscode://file/etc/passwd' },
    { name: 'a mail client', url: 'mailto:someone@example.com' },
    { name: 'a shell-adjacent scheme', url: 'smb://host/share' },
    { name: 'a bare path', url: '/etc/passwd' },
    { name: 'no scheme at all', url: 'example.com' },
    { name: 'a scheme with no host', url: 'http://' },
    { name: 'nothing', url: '' },
  ];

  for (const { name, url } of REFUSED) {
    test(`POST /api/open-url refuses ${name}`, async ({ request }) => {
      const res = await request.post('/api/open-url', { data: { url } });

      expect(res.status()).toBe(400);
      const body = await res.json();
      expect(typeof body.error).toBe('string');
      expect(body.error.length).toBeGreaterThan(0);
    });
  }

  test('POST /api/open-url refuses a body with no url field', async ({
    request,
  }) => {
    const res = await request.post('/api/open-url', { data: {} });
    expect(res.status()).toBe(400);
  });

  test('POST /api/open-url refuses a malformed body', async ({ request }) => {
    const res = await request.post('/api/open-url', {
      headers: { 'Content-Type': 'application/json' },
      data: 'not json',
    });
    expect(res.status()).toBe(400);
  });

  // The endpoint is an action, so it exists under POST and nothing else — a
  // GET must not be able to trigger a launch by being prefetched or linked.
  test('GET /api/open-url is not allowed', async ({ request }) => {
    const res = await request.get('/api/open-url');
    expect(res.status()).toBe(405);
  });
});
