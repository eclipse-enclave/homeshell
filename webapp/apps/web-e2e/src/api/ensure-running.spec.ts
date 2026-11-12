import { expect, test } from '@playwright/test';
import { execFile, execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { createServer } from 'node:net';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { daemonDir, homeshellBin, repoRoot } from '../e2e-env';
import {
  buildDaemonVariant,
  disposeIsolatedHome,
  isolatedHome,
  runtimeEnv,
} from '../isolated-daemon';

/**
 * Black-box coverage of `homeshell ensure-running` (the desktop shell's daemon
 * bootstrap): with no daemon it spawns one, prints its loopback URL, and that
 * URL serves the UI document (setting the hs_api_token cookie) so a webview
 * authenticates like a browser. Survives a daemon rewrite (CLI + HTTP only).
 */
const execFileAsync = promisify(execFile);
let binary = homeshellBin;
const homeDir = isolatedHome('ens');

// -addr=127.0.0.1:0 asks the spawned daemon to bind an ephemeral port so this
// spec never collides with the fixed :8420 default (or another instance).
const ADDR_ARG = '-addr=127.0.0.1:0';

// The daemons ensure-running spawns inherit this, so their bridges stay in
// their home, where the shared teardown looks.
const ensureRunning = (home: string, ...args: string[]) =>
  execFileAsync(binary, ['ensure-running', `-home-dir=${home}`, ...args], {
    env: { ...process.env, ...runtimeEnv(home) },
  });

test.describe('ensure-running', () => {
  test.beforeAll(() => {
    // A fresh checkout has no built UI embedded in the daemon: the GET / 200
    // + hs_api_token cookie assertion below needs a real index.html, so build
    // and embed it first if it's missing, then build a daemon of this spec's
    // own (the run's build was made without it). On a normal dev tree the
    // artifacts are already present and this is a no-op, keeping the common
    // case fast.
    const embeddedIndex = join(daemonDir, 'internal/webui/dist/index.html');
    if (!existsSync(embeddedIndex)) {
      test.setTimeout(180_000);
      execFileSync('make', ['build-webapp'], { cwd: repoRoot });
      execFileSync('make', ['embed'], { cwd: repoRoot });
      binary = buildDaemonVariant('ensure-ui');
    }
  });

  // Stops the daemon ensure-running spawned (by the pid it published, should
  // `stop` not reach it) and its bridges, and deletes its home.
  test.afterAll(() => disposeIsolatedHome(homeDir));

  test('spawns a daemon and prints a URL that serves the UI', async () => {
    const { stdout } = await ensureRunning(homeDir, ADDR_ARG, '--json');
    const result = JSON.parse(stdout) as { url: string; spawned: boolean };
    expect(result.spawned).toBe(true);
    expect(result.url).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/$/);

    // The URL is a top-level document load: the daemon bootstraps the auth
    // cookie on it, so a webview then authenticates transparently.
    const doc = await fetch(result.url, { headers: { Accept: 'text/html' }, redirect: 'manual' });
    expect(doc.status).toBe(200);
    expect(doc.headers.get('set-cookie') ?? '').toContain('hs_api_token');

    // A second call attaches to the same daemon without spawning.
    const again = await ensureRunning(homeDir, ADDR_ARG, '--json');
    const second = JSON.parse(again.stdout) as { url: string; spawned: boolean };
    expect(second.spawned).toBe(false);
    expect(second.url).toBe(result.url);
  });

  /**
   * A daemon that cannot bind writes the reason to its stderr and exits. When
   * that stream was discarded the only symptom left was `ensure-running`
   * burning its whole -timeout, so this pins both halves of the fix: it fails
   * well inside the timeout, and the bind error reaches the caller.
   */
  test('reports why a spawned daemon could not start', async () => {
    const occupiedHome = isolatedHome('busy');
    const squatter = createServer();
    const port = await new Promise<number>((resolvePort, rejectPort) => {
      squatter.once('error', rejectPort);
      squatter.listen(0, '127.0.0.1', () => {
        const address = squatter.address();
        if (address === null || typeof address === 'string') {
          rejectPort(new Error('expected a TCP address from the squatting listener'));
          return;
        }
        resolvePort(address.port);
      });
    });

    try {
      const timeoutSeconds = 30;
      const startedAt = Date.now();
      const failure = await ensureRunning(
        occupiedHome,
        `-addr=127.0.0.1:${port}`,
        `-timeout=${timeoutSeconds}s`,
      ).then(
        () => undefined,
        (error: { code?: number; stderr?: string }) => error,
      );
      const elapsedSeconds = (Date.now() - startedAt) / 1000;

      expect(failure, 'ensure-running should fail when the port is taken').toBeDefined();
      expect(failure?.code).toBe(1);
      expect(failure?.stderr ?? '').toContain('address already in use');
      // Generous margin over the observed sub-second exit, tight enough that
      // waiting out the timeout (the old behavior) still fails the assertion.
      expect(elapsedSeconds).toBeLessThan(timeoutSeconds / 2);

      // The full output stays on disk for anything the message truncates.
      expect(existsSync(join(occupiedHome, 'daemon.log'))).toBe(true);
    } finally {
      await new Promise((closed) => squatter.close(closed));
      await disposeIsolatedHome(occupiedHome);
    }
  });
});
