/// <reference types='vitest' />
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';
import type { IncomingMessage } from 'node:http';

/**
 * Whether a proxied request came from a page this dev server served, and may therefore
 * be spoken for: an `Origin` equal to the host it was addressed to (comparing against
 * the request's own `Host` avoids hardcoding the dev port, which `--port` overrides in
 * e2e), or no `Origin` at all.
 *
 * Absent must pass, including for writes — but not for the reason it first appears.
 * Browsers *do* send `Origin` on a same-origin `PUT`/`POST` (it is appended for every
 * non-GET/HEAD method), so the real UI's writes take the branch above. What arrives
 * without one is the **ui e2e harness**: those specs drive `PUT /api/settings` and the
 * session POSTs through Playwright's `APIRequestContext`, a Node HTTP client with no
 * `Origin`, and rely on this proxy to inject the token. Restricting the absent case to
 * GET/HEAD failed 5 of them.
 *
 * That is still safe, because a *cross-site* write always carries an `Origin` (measured,
 * including a no-cors `fetch` and a form POST) and is therefore refused above. Two
 * things do slip through the absent branch: any non-browser local client, which gains
 * nothing it could not get by reading the 0600 state file, and a cross-site no-cors
 * *GET*, whose response stays opaque (the proxy sends no CORS headers). Both are
 * dev-only. Tightening this means giving the ui specs their own token instead.
 */
function sameOriginAsDevServer(req: IncomingMessage): boolean {
  const origin = req.headers.origin;
  if (!origin) return true;
  try {
    return new URL(origin).host === req.headers.host;
  } catch {
    return false;
  }
}

export default defineConfig(() => ({
  root: import.meta.dirname,
  cacheDir: '../../node_modules/.vite/apps/web',
  server: {
    port: 4200,
    host: 'localhost',
    proxy: {
      // The HomeShell daemon; see the root Makefile's `dev` target. The daemon's
      // loopback guard rejects a foreign Origin, so changeOrigin rewrites the
      // proxied Host to the daemon and the browser's dev-server Origin is stripped
      // — keeping the browser -> vite -> daemon path transparent to the guard.
      //
      // In dev/e2e vite serves the document, so the daemon never gets to set its
      // hs_api_token cookie in the browser. Instead the proxy injects the API
      // token as a Bearer header on every proxied /api request (the token comes
      // from HOMESHELL_API_TOKEN, set by `make dev` / the e2e config), so the
      // browser -> vite -> daemon path authenticates transparently. In production
      // the daemon serves the document and sets the cookie itself.
      //
      // Both of those only apply to requests that came from a page THIS dev server
      // served. Doing them unconditionally makes the proxy a confused deputy: it
      // would present a valid token and no Origin on behalf of any web page in the
      // world, so a cross-site `fetch(..., {mode: 'no-cors', method: 'POST'})` — no
      // preflight, since a JSON body rides in a text/plain POST — reached the daemon
      // as a trusted local client and could create projects and sessions. A foreign
      // Origin is therefore forwarded untouched and unvouched-for, which the daemon's
      // own guard then refuses.
      '/api': {
        target: process.env.HOMESHELL_DAEMON_URL ?? 'http://127.0.0.1:8420',
        changeOrigin: true,
        configure: (proxy) => {
          // Trimmed once, here, and used by both the warning and the injection: the
          // daemon trims its own intake, so a whitespace-only value is no token — and
          // injecting `Bearer   ` while the warning stayed quiet (`!"   "` is false) was
          // the last silent case of that rule, which serve.go and the e2e config both
          // apply on their side.
          const token = process.env.HOMESHELL_API_TOKEN?.trim();
          // Say so once at startup rather than injecting nothing per request: without the
          // token the UI renders and then 401s on every call, with nothing pointing at
          // the cause. `make dev` and the e2e config both set it, so this fires for a
          // bare `vite dev` — the daemon warns about the same class of misconfiguration
          // on its own side.
          if (!token) {
            console.warn(
              '[homeshell] HOMESHELL_API_TOKEN is not set: /api requests will be proxied ' +
                'without a token and the daemon will answer 401. Start the dev server ' +
                'through `make dev` (or export the token the daemon was started with).',
            );
          }
          proxy.on('proxyReq', (proxyReq, req) => {
            if (!sameOriginAsDevServer(req)) return;
            proxyReq.removeHeader('origin');
            if (token) {
              proxyReq.setHeader('authorization', `Bearer ${token}`);
            }
          });
        },
      },
    },
  },
  preview: {
    port: 4200,
    host: 'localhost',
  },
  plugins: [react()],
  // Uncomment this if you are using workers.
  // worker: {
  //  plugins: [],
  // },
  build: {
    outDir: './dist',
    emptyOutDir: true,
    reportCompressedSize: true,
    commonjsOptions: {
      transformMixedEsModules: true,
    },
  },
  test: {
    name: '@homeshell/web',
    watch: false,
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./vitest.setup.ts'],
    include: ['{src,tests}/**/*.{test,spec}.{js,mjs,cjs,ts,mts,cts,jsx,tsx}'],
    reporters: ['default'],
    coverage: {
      reportsDirectory: './test-output/vitest/coverage',
      provider: 'v8' as const,
    },
  },
}));
