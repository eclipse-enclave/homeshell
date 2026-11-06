import { expect, test } from '@playwright/test';
import { execFile, execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { promisify } from 'node:util';

/**
 * Black-box coverage of `homeshell ensure-running` (the desktop shell's daemon
 * bootstrap): with no daemon it spawns one, prints its loopback URL, and that
 * URL serves the UI document (setting the hs_api_token cookie) so a webview
 * authenticates like a browser. Survives a daemon rewrite (CLI + HTTP only).
 */
const execFileAsync = promisify(execFile);
const repoRoot = resolve(__dirname, '../../../../..');
const daemonDir = join(repoRoot, 'daemon');
const binary = join(tmpdir(), 'homeshell-ensure-e2e');
const homeDir = mkdtempSync(join(tmpdir(), 'homeshell-ensure-'));
const stateFile = join(homeDir, 'daemon.json');

// -addr=127.0.0.1:0 asks the spawned daemon to bind an ephemeral port so this
// spec never collides with the fixed :8420 default (or another instance).
const ADDR_ARG = '-addr=127.0.0.1:0';

test.describe('ensure-running', () => {
  test.beforeAll(() => {
    test.setTimeout(120_000);

    // A fresh checkout has no built UI embedded in the daemon: the GET / 200
    // + hs_api_token cookie assertion below needs a real index.html, so build
    // and embed it first if it's missing. On a normal dev tree the artifacts
    // are already present and this is a no-op, keeping the common case fast.
    const embeddedIndex = join(daemonDir, 'internal/webui/dist/index.html');
    if (!existsSync(embeddedIndex)) {
      execFileSync('make', ['build-webapp'], { cwd: repoRoot });
      execFileSync('make', ['embed'], { cwd: repoRoot });
    }

    execFileSync('go', ['build', '-o', binary, './cmd/homeshell'], { cwd: daemonDir });
  });

  test.afterAll(async () => {
    await execFileAsync(binary, ['stop', `-home-dir=${homeDir}`]).catch(() => undefined);

    // Safety net: if `stop` failed to reach a still-running daemon (e.g. a
    // failed assertion left it dangling), read the pid it published and kill
    // it directly so a leaked daemon can't block future runs.
    if (existsSync(stateFile)) {
      try {
        const state = JSON.parse(readFileSync(stateFile, 'utf8')) as { pid?: number };
        if (state.pid) process.kill(state.pid, 'SIGKILL');
      } catch {
        // Best-effort cleanup only.
      }
    }
  });

  test('spawns a daemon and prints a URL that serves the UI', async () => {
    const { stdout } = await execFileAsync(binary, [
      'ensure-running',
      `-home-dir=${homeDir}`,
      ADDR_ARG,
      '--json',
    ]);
    const result = JSON.parse(stdout) as { url: string; spawned: boolean };
    expect(result.spawned).toBe(true);
    expect(result.url).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/$/);

    // The URL is a top-level document load: the daemon bootstraps the auth
    // cookie on it, so a webview then authenticates transparently.
    const doc = await fetch(result.url, { headers: { Accept: 'text/html' }, redirect: 'manual' });
    expect(doc.status).toBe(200);
    expect(doc.headers.get('set-cookie') ?? '').toContain('hs_api_token');

    // A second call attaches to the same daemon without spawning.
    const again = await execFileAsync(binary, [
      'ensure-running',
      `-home-dir=${homeDir}`,
      ADDR_ARG,
      '--json',
    ]);
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
    const occupiedHome = mkdtempSync(join(tmpdir(), 'homeshell-ensure-busy-'));
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
      const failure = await execFileAsync(binary, [
        'ensure-running',
        `-home-dir=${occupiedHome}`,
        `-addr=127.0.0.1:${port}`,
        `-timeout=${timeoutSeconds}s`,
      ]).then(
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
      await execFileAsync(binary, ['stop', `-home-dir=${occupiedHome}`]).catch(() => undefined);
    }
  });
});
