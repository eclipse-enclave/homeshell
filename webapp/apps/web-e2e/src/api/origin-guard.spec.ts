import { expect, test } from '@playwright/test';
import { connect } from 'node:net';
import { API_TOKEN, authHeader } from '../api-token';

/**
 * Black-box tests for the daemon's loopback guard (Host allow-list +
 * cross-origin/CSRF Origin rejection). The guard is transparent to a real
 * loopback client — the default `request` fixture and the vite-proxied UI path
 * both keep working — while a foreign Host (DNS-rebinding) or a foreign Origin
 * (CSRF) is refused with 403.
 *
 * Host/port are always derived from the project `baseURL` so the assertions
 * can't drift from the configured daemon port. Overriding the Host header is
 * something Playwright's undici-based `request` fixture refuses, so those cases
 * drop to a raw TCP socket and parse the HTTP status line directly.
 */

/**
 * Sends a raw HTTP/1.1 GET with a chosen Host header and resolves to the verbatim
 * response text (status line and headers included), so a test can assert on either.
 * The API token rides along so a request the guard *allows* reaches the route with a
 * 200 rather than stopping at auth's 401 — the guard is the outermost middleware, so
 * a rejection is still an unambiguous 403.
 */
function rawExchange(
  baseURL: string,
  hostHeader: string,
  path: string,
  extraHeaders: Record<string, string> = {},
): Promise<string> {
  const url = new URL(baseURL);
  const port = Number(url.port);
  const headers = Object.entries({ ...authHeader(), ...extraHeaders })
    .map(([name, value]) => `${name}: ${value}\r\n`)
    .join('');
  return new Promise((resolve, reject) => {
    const socket = connect({ host: url.hostname, port }, () => {
      socket.write(
        `GET ${path} HTTP/1.1\r\nHost: ${hostHeader}\r\n${headers}Connection: close\r\n\r\n`,
      );
    });
    let data = '';
    socket.setEncoding('utf8');
    socket.on('data', (chunk) => {
      data += chunk;
    });
    socket.on('end', () => resolve(data));
    socket.on('error', reject);
  });
}

/** Sends a raw HTTP/1.1 GET with a chosen Host header; resolves to the status code. */
async function rawGet(
  baseURL: string,
  hostHeader: string,
  path: string,
): Promise<number> {
  const response = await rawExchange(baseURL, hostHeader, path);
  const statusLine = response.split('\r\n', 1)[0];
  const status = Number(statusLine.split(' ')[1]);
  if (!Number.isInteger(status)) {
    throw new Error(`could not parse status line: ${statusLine}`);
  }
  return status;
}

test.describe('loopback Host + Origin guard', () => {
  test('transparent to a real loopback client (default GET /api/hello)', async ({
    request,
  }) => {
    const res = await request.get('/api/hello');
    expect(res.status()).toBe(200);
  });

  test('rejects a foreign Host (DNS-rebinding)', async ({ baseURL }) => {
    const status = await rawGet(baseURL!, 'evil.com', '/api/hello');
    expect(status).toBe(403);
  });

  // A loopback literal on another port is a port-forward or a loopback proxy
  // ("ssh -L 9999:localhost:<port>"), not a rebinding attempt: no re-resolution
  // can turn a foreign name into "localhost", so the port need not be the
  // daemon's. Rejecting it made the UI render and then 403 on every call.
  test('accepts a loopback Host on a forwarded port', async ({ baseURL }) => {
    const port = Number(new URL(baseURL!).port);
    const status = await rawGet(baseURL!, `127.0.0.1:${port + 1}`, '/api/hello');
    expect(status).toBe(200);
  });

  // Hostnames are not case-sensitive and a non-browser client forwards the case it
  // was given, so an uppercase loopback Host must pass the literal check.
  test('accepts an uppercase loopback Host', async ({ baseURL }) => {
    const port = new URL(baseURL!).port;
    const status = await rawGet(baseURL!, `LOCALHOST:${port}`, '/api/hello');
    expect(status).toBe(200);
  });

  test('rejects a cross-origin read (Origin: http://evil.com)', async ({
    request,
  }) => {
    const res = await request.get('/api/hello', {
      headers: { Origin: 'http://evil.com' },
    });
    expect(res.status()).toBe(403);
  });

  test('rejects a cross-origin mutation (POST with foreign Origin)', async ({
    request,
  }) => {
    const res = await request.post('/api/projects', {
      headers: { Origin: 'http://evil.com' },
      data: { path: '/tmp' },
    });
    expect(res.status()).toBe(403);
  });

  test('allows the daemon\'s own Origin', async ({ request, baseURL }) => {
    const res = await request.get('/api/hello', {
      headers: { Origin: new URL(baseURL!).origin },
    });
    expect(res.status()).toBe(200);
  });

  test('allows a loopback-alias Origin (http://localhost:<port>)', async ({
    request,
    baseURL,
  }) => {
    const port = new URL(baseURL!).port;
    const res = await request.get('/api/hello', {
      headers: { Origin: `http://localhost:${port}` },
    });
    expect(res.status()).toBe(200);
  });

  // A page served from another loopback port is same-*site* (so the SameSite=Strict
  // token cookie would ride along) but cross-origin: the Origin must address the
  // same port the request itself was addressed to. This is the local CSRF defense.
  test('rejects a cross-port loopback Origin', async ({ request }) => {
    const res = await request.get('/api/hello', {
      headers: { Origin: 'http://localhost:3000' },
    });
    expect(res.status()).toBe(403);
  });

  // The daemon serves plain http, but an https Origin on the same loopback
  // host+port is accepted for parity with the same-origin check: a
  // TLS-terminating proxy on loopback can legitimately present one.
  test('allows an https Origin on the loopback host+port', async ({
    request,
    baseURL,
  }) => {
    const url = new URL(baseURL!);
    const res = await request.get('/api/hello', {
      headers: { Origin: `https://${url.host}` },
    });
    expect(res.status()).toBe(200);
  });

  // The served document is outside the /api guard, so the Host check is repeated in
  // the cookie bootstrap: without it a DNS-rebound page collects the token cookie
  // under its own hostname and the browser then sends the token to the attacker's
  // own server, which HttpOnly does nothing to prevent.
  test('a rebound Host gets no token cookie on the document', async ({
    baseURL,
  }) => {
    const response = await rawExchange(baseURL!, 'evil.com', '/', {
      Accept: 'text/html',
    });
    expect(response.toLowerCase()).not.toContain('set-cookie');
  });

  test('a loopback Host still gets the token cookie on the document', async ({
    baseURL,
  }) => {
    const port = new URL(baseURL!).port;
    const response = await rawExchange(baseURL!, `localhost:${port}`, '/', {
      Accept: 'text/html',
    });
    expect(response).toContain(`hs_api_token=${API_TOKEN}`);
  });

  test('SSE still streams through the guard', async ({ baseURL }) => {
    const controller = new AbortController();
    try {
      const res = await fetch(new URL('/api/events', baseURL), {
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
