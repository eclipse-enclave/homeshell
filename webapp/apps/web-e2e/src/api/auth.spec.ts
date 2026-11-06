import { expect, test } from '@playwright/test';
import { API_TOKEN, authHeader } from '../api-token';

/**
 * Black-box coverage of the /api bearer-token authentication (docs/API.md, "Authentication").
 * Every /api request must present the token as an "Authorization: Bearer
 * <token>" header or the hs_api_token cookie; a missing or wrong credential is
 * rejected with 401. These cases use a raw fetch() so they control the headers
 * directly — the `request` fixture attaches the token by default (see
 * playwright.config.ts), which is exactly what keeps the other api specs green.
 */
test.describe('API token authentication', () => {
  test('GET /api/hello without a credential is 401', async ({ baseURL }) => {
    const res = await fetch(`${baseURL}/api/hello`);
    expect(res.status).toBe(401);
  });

  test('GET /api/hello with the correct Bearer token is 200', async ({
    baseURL,
  }) => {
    const res = await fetch(`${baseURL}/api/hello`, {
      headers: { ...authHeader() },
    });
    expect(res.status).toBe(200);
    const body = (await res.json()) as { message: string };
    expect(body.message).toContain('HomeShell');
  });

  test('GET /api/hello with a wrong token is 401', async ({ baseURL }) => {
    const res = await fetch(`${baseURL}/api/hello`, {
      headers: { Authorization: 'Bearer not-the-real-token' },
    });
    expect(res.status).toBe(401);
  });

  // A browser whose hs_api_token cookie went stale after a daemon restart
  // rotated the per-startup token would 401 forever, because a native
  // EventSource never retries a 401; the 401 re-issues the fresh cookie so the
  // next reconnect authenticates. See docs/API.md, "Authentication".
  test('a 401 with a stale cookie self-heals by re-issuing the fresh cookie', async ({
    baseURL,
  }) => {
    const res = await fetch(`${baseURL}/api/hello`, {
      headers: { Cookie: 'hs_api_token=wrong' },
    });
    expect(res.status).toBe(401);
    expect(res.headers.get('set-cookie')).toContain(`hs_api_token=${API_TOKEN}`);
  });

  // A cookie is not a credential here — a page on another loopback port can mint one,
  // since cookies are not port-scoped — so the self-heal also requires the request to
  // be same-origin. A subresource request from such a page is same-*site*, which a
  // browser cannot forge. Note the surrounding tests exercise the absent-metadata
  // branch, which is the non-browser client.
  test('a 401 from a non-same-origin fetch does not hand out the token', async ({
    baseURL,
  }) => {
    for (const site of ['same-site', 'cross-site', 'none']) {
      const res = await fetch(`${baseURL}/api/hello`, {
        headers: { Cookie: 'hs_api_token=minted', 'Sec-Fetch-Site': site },
      });
      expect(res.status).toBe(401);
      expect(res.headers.get('set-cookie')).toBeNull();
    }
  });

  test('a 401 from a same-origin fetch still self-heals', async ({ baseURL }) => {
    const res = await fetch(`${baseURL}/api/hello`, {
      headers: { Cookie: 'hs_api_token=stale', 'Sec-Fetch-Site': 'same-origin' },
    });
    expect(res.status).toBe(401);
    expect(res.headers.get('set-cookie')).toContain(`hs_api_token=${API_TOKEN}`);
  });

  // The self-heal is deliberately limited to a caller that already holds a
  // cookie: a request with no credential at all must not be able to turn a 401
  // into a usable token.
  test('a 401 with no cookie at all does not hand out the token', async ({
    baseURL,
  }) => {
    const res = await fetch(`${baseURL}/api/hello`);
    expect(res.status).toBe(401);
    expect(res.headers.get('set-cookie')).toBeNull();
  });

  // An empty cookie value is not a stale credential, it is a guessed cookie name:
  // Go's request.Cookie returns no error for `hs_api_token=`, so gating the
  // self-heal on the cookie's presence rather than its value reopened the
  // zero-credential path the case above closes.
  test('a 401 with an empty cookie value does not hand out the token', async ({
    baseURL,
  }) => {
    for (const cookie of ['hs_api_token=', 'hs_api_token=""']) {
      const res = await fetch(`${baseURL}/api/hello`, { headers: { Cookie: cookie } });
      expect(res.status).toBe(401);
      expect(res.headers.get('set-cookie')).toBeNull();
    }
  });

  // A non-Bearer Authorization header must not shadow a valid cookie: the
  // credential extraction falls through to the cookie.
  test('a non-Bearer Authorization header falls through to a valid cookie (200)', async ({
    baseURL,
  }) => {
    const res = await fetch(`${baseURL}/api/hello`, {
      headers: { Authorization: 'Basic x', Cookie: `hs_api_token=${API_TOKEN}` },
    });
    expect(res.status).toBe(200);
  });

  // The production browser path: the daemon serves the document and seeds the
  // token cookie on it, so the UI's fetch/EventSource calls authenticate with no
  // web-UI code involved. The `ui` project cannot cover this — it goes through
  // vite, which serves its own document and injects a Bearer header instead. The
  // cookie is set by middleware wrapping the web-UI handler, so this holds even
  // in an e2e build with no embedded dist (whatever status the document gets).
  test('a document navigation bootstraps the hs_api_token cookie', async ({
    baseURL,
  }) => {
    const res = await fetch(`${baseURL}/`, { headers: { Accept: 'text/html' } });
    expect(res.headers.get('set-cookie')).toContain(`hs_api_token=${API_TOKEN}`);
    // Hardened: unreadable from JS and never sent cross-site.
    expect(res.headers.get('set-cookie')).toContain('HttpOnly');
    expect(res.headers.get('set-cookie')).toContain('SameSite=Strict');
  });

  test('a non-document request does not bootstrap the cookie', async ({
    baseURL,
  }) => {
    const res = await fetch(`${baseURL}/`, {
      headers: { Accept: 'application/json' },
    });
    expect(res.headers.get('set-cookie')).toBeNull();
  });

  // Accept is CORS-safelisted, so "asks for html" is not "is a navigation". A script's
  // fetch() announces itself with Sec-Fetch-Dest and cannot forge it, and a non-GET is
  // not a navigation at all; neither may collect the token.
  test('a scripted fetch and a POST do not bootstrap the cookie', async ({
    baseURL,
  }) => {
    const scripted = await fetch(`${baseURL}/`, {
      headers: { Accept: 'text/html', 'Sec-Fetch-Dest': 'empty' },
    });
    expect(scripted.headers.get('set-cookie')).toBeNull();

    const posted = await fetch(`${baseURL}/`, {
      method: 'POST',
      headers: { Accept: 'text/html' },
    });
    expect(posted.headers.get('set-cookie')).toBeNull();
  });

  // Cookies are not port-scoped, so a page on another loopback port can add a second
  // hs_api_token at a longer path and would shadow the real one if only the first were
  // read — locking the UI out at 401 with nothing to explain it.
  test('a shadow cookie does not lock out the real token', async ({
    baseURL,
  }) => {
    const res = await fetch(`${baseURL}/api/hello`, {
      headers: { Cookie: `hs_api_token=shadow; hs_api_token=${API_TOKEN}` },
    });
    expect(res.status).toBe(200);
  });

  test('SSE GET /api/events without a token is 401', async ({ baseURL }) => {
    const res = await fetch(`${baseURL}/api/events`, {
      headers: { accept: 'text/event-stream' },
    });
    expect(res.status).toBe(401);
  });

  test('SSE GET /api/events with a token streams events', async ({
    baseURL,
  }) => {
    const controller = new AbortController();
    try {
      const res = await fetch(`${baseURL}/api/events`, {
        signal: controller.signal,
        headers: { accept: 'text/event-stream', ...authHeader() },
      });
      expect(res.status).toBe(200);
      expect(res.headers.get('content-type')).toContain('text/event-stream');
    } finally {
      controller.abort();
    }
  });
});
