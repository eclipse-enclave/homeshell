import { defineConfig, devices } from '@playwright/test';
import { dirname, resolve } from 'node:path';
import {
  daemonUrl,
  e2eRoot,
  homeshellBin,
  sharedRuntimeDir,
  webUrl,
} from './src/e2e-env';

/**
 * Two end-to-end suites (see docs/ARCHITECTURE.md, "Principles"):
 *
 * - `api` runs black-box against the daemon's public HTTP API — no browser,
 *   no UI. These tests must survive a potential daemon rewrite.
 * - `ui` drives the web app in Chromium against the vite dev server, which
 *   proxies /api to the daemon (the same wiring `make dev` uses).
 *
 * Both servers are started automatically; already-running dev servers are
 * reused outside CI. Their ports, the daemon build and every temp file come
 * from this checkout's e2e root (src/e2e-env.ts), so two checkouts can run the
 * suite at once.
 */
const DAEMON_URL = daemonUrl;
const WEB_URL = webUrl;

// Pinned for the whole run: the runner and every worker evaluate this file, and
// the workers inherit the runner's env. globalSetup compares the build's mtime
// with the run's start to tell a fresh build from a reused daemon's old one.
process.env.HOMESHELL_E2E_ROOT = e2eRoot;
process.env.HOMESHELL_E2E_BIN = homeshellBin;
process.env.HOMESHELL_E2E_RUN_START ||= String(Date.now());

// The daemon requires an API token on every /api request (Bearer header or the
// hs_api_token cookie). Pin a fixed token for e2e so the api project can attach
// it as a default header, the vite proxy can inject it for the browser path, and
// the raw fetch()/socket cases in the specs can read it from process.env. Set it
// on process.env so it is inherited by both webServer subprocesses and the
// Playwright workers that run the spec files.
//
// Trimmed and `||`, not `??=`: an `export HOMESHELL_API_TOKEN=` in the shell is an empty
// string rather than undefined, which `??=` keeps. The specs would then send a bare
// "Bearer ", the daemon would generate its own token instead, and every api spec would
// 401 — while the readiness probe still reported ready, because it 401s in the healthy
// case too. The daemon trims its own intake for the same reason, so a whitespace-only
// value is no value here either.
process.env.HOMESHELL_API_TOKEN =
  process.env.HOMESHELL_API_TOKEN?.trim() || 'e2e-fixed-api-token';
const API_TOKEN = process.env.HOMESHELL_API_TOKEN;

// All e2e daemon state lives under db/e2e (gitignored): state.json, agents.json,
// daemon.json and settings.json all resolve under -home-dir, so
// e2e never reads or writes the developer's real data directory
// (docs/ARCHITECTURE.md, "Persistence").
const E2E_HOME_DIR = resolve(__dirname, '../../../db/e2e');

export default defineConfig({
  testDir: './src',
  outputDir: './test-output/playwright',
  fullyParallel: true,
  globalSetup: './src/global-setup.ts',
  globalTeardown: './src/global-teardown.ts',
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: [['list']],
  projects: [
    {
      name: 'api',
      testMatch: /api\/.*\.spec\.ts/,
      use: {
        baseURL: DAEMON_URL,
        // Every request through the `request` fixture carries the API token, so
        // existing black-box specs keep working transparently. Cases that drive a
        // raw socket or bare fetch() attach the token themselves.
        extraHTTPHeaders: { Authorization: `Bearer ${API_TOKEN}` },
      },
    },
    {
      name: 'ui',
      testMatch: /ui\/.*\.spec\.ts/,
      use: { ...devices['Desktop Chrome'], baseURL: WEB_URL },
    },
  ],
  webServer: [
    {
      // -demo=true: the UI and provider e2e specs assert on the seeded
      // workstreams and the session simulator, which the fake provider supplies.
      //
      // Pre-seed settings.json with notifications disabled so the always-running
      // demo cycler does not fire real OS-native desktop notifications on the
      // developer's machine during the test run (the default style is native).
      // The daemon merges this over its defaults at startup; the notification
      // specs opt into the in-app style (which never reaches the OS) themselves.
      //
      // The directory is wiped first, so every started daemon begins from the
      // same state: a run cut short leaves its state file and provider data
      // behind, and they would otherwise pile up into the next run's. A reused
      // server skips this command, so a live daemon's directory is never wiped.
      //
      // This command makes the run's one daemon build, which every spec helper
      // uses too: web servers start before globalSetup, so it cannot be built
      // there (globalSetup only rebuilds it when a reused daemon skipped this).
      // `touch`: go build leaves an up-to-date file alone, and globalSetup reads
      // the mtime. `exec`, so the daemon itself gets Playwright's SIGTERM.
      command: [
        `mkdir -p "${dirname(homeshellBin)}"`,
        `go build -o "${homeshellBin}" ./cmd/homeshell`,
        `touch "${homeshellBin}"`,
        `rm -rf "${E2E_HOME_DIR}"`,
        `mkdir -p "${E2E_HOME_DIR}"`,
        `printf '%s' '{"schemaVersion":1,"settings":{"notifications":{"style":"none"}}}' > "${E2E_HOME_DIR}/settings.json"`,
        `exec "${homeshellBin}" serve -demo=true -home-dir=${E2E_HOME_DIR} -addr=${new URL(DAEMON_URL).host}`,
      ].join(' && '),
      cwd: '../../../daemon',
      // Readiness probe. Note this polls an *authenticated* endpoint without a
      // token, so every poll is a 401 (visible as a WARN in the daemon log at
      // startup) and only counts as "server up" because Playwright treats any
      // status below 404 as ready. That is intentional — the probe just needs the
      // port to answer — but if a future Playwright tightens the accepted statuses
      // this turns into a mystery startup timeout. There is no webServer option to
      // attach a header, so the fix would be a token-free readiness endpoint.
      url: `${DAEMON_URL}/api/hello`,
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
      // Lets the daemon shut down instead of a SIGKILL to its process group.
      gracefulShutdown: { signal: 'SIGTERM', timeout: 5_000 },
      env: {
        ...process.env,
        // Its bridges' sockets stay in the e2e root, where globalTeardown
        // stops them.
        XDG_RUNTIME_DIR: sharedRuntimeDir,
        // A source-built daemon has no release channel and never checks for
        // updates; should one ever be stamped, keep its release check off the
        // public internet. The Go updater tests cover the real update flow.
        HOMESHELL_UPDATE_BASE_URL: 'http://127.0.0.1:9/',
        // The native folder chooser opens a real window on this machine and the
        // request stays open until somebody answers it, so one Browse click in a
        // spec that forgot to stub /api/browse/native would hang the run on a
        // dialog. Off at the daemon, the endpoint reports 501 and the UI falls
        // back to the in-app picker; the specs that exercise the native outcomes
        // still stub the route themselves.
        HOMESHELL_NO_NATIVE_DIALOG: '1',
        // Make the fake provider's session creation slow enough to observe, and
        // have it stream synthetic build output while it runs. Without this a
        // fake create is instant, so the starting-session placeholder and its
        // log tail exist for microseconds and no black-box spec can see them.
        // The number is a budget for the whole synthetic build, so it bounds how
        // long the specs that wait for one take.
        HOMESHELL_FAKE_SESSION_STARTUP_MS: '1200',
        // Pin the API token so the daemon uses the same secret the specs present.
        HOMESHELL_API_TOKEN: API_TOKEN,
      },
    },
    {
      // vite directly, not `nx dev`: Nx detaches continuous tasks into their
      // own session, so Playwright's teardown would leak the vite process.
      // --strictPort: vite would otherwise move to the next free port while
      // Playwright keeps polling this one.
      command: `pnpm exec vite dev --host ${new URL(WEB_URL).hostname} --port ${new URL(WEB_URL).port} --strictPort`,
      cwd: '../web',
      url: WEB_URL,
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
      env: {
        ...process.env,
        HOMESHELL_DAEMON_URL: DAEMON_URL,
        // The vite proxy injects this token as a Bearer header on proxied /api
        // requests (see vite.config.mts), so the browser path authenticates in dev.
        HOMESHELL_API_TOKEN: API_TOKEN,
      },
    },
  ],
});
