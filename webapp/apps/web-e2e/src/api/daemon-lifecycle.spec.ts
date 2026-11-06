import { expect, test } from '@playwright/test';
import { execFile, execFileSync, spawn } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { promisify } from 'node:util';

/**
 * Black-box coverage of the daemon lifecycle (Issue #1): the daemon publishes a
 * discovery state file, refuses a second instance, and stops cleanly. Clients
 * find the daemon through that file instead of a hardcoded port, so this suite
 * would survive a daemon rewrite (docs/ARCHITECTURE.md, "Principles").
 *
 * It runs its own daemon on an ephemeral port (`-addr 127.0.0.1:0`) with an
 * isolated state file, independent of the daemon playwright.config.ts starts.
 */
const execFileAsync = promisify(execFile);
const daemonDir = resolve(__dirname, '../../../../../daemon');
const binary = join(tmpdir(), 'homeshell-lifecycle-e2e');
const homeDir = mkdtempSync(join(tmpdir(), 'homeshell-lifecycle-'));
const stateFile = join(homeDir, 'daemon.json');

interface CliResult {
  code: number;
  stdout: string;
  stderr: string;
}

async function runCli(args: string[]): Promise<CliResult> {
  try {
    const { stdout, stderr } = await execFileAsync(binary, args);
    return { code: 0, stdout, stderr };
  } catch (err) {
    const e = err as { code?: number; stdout?: string; stderr?: string };
    return {
      code: e.code ?? 1,
      stdout: e.stdout ?? '',
      stderr: e.stderr ?? '',
    };
  }
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function waitFor(
  predicate: () => boolean,
  timeoutMs: number,
): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return true;
    await sleep(50);
  }
  return predicate();
}

interface DaemonState {
  address: string;
  pid: number;
  version: string;
  startedAt: string;
  token: string;
}

const readState = (): DaemonState =>
  JSON.parse(readFileSync(stateFile, 'utf8'));

test.describe('daemon lifecycle', () => {
  let daemon: ReturnType<typeof spawn> | undefined;

  test.beforeAll(() => {
    test.setTimeout(120_000);
    execFileSync('go', ['build', '-o', binary, './cmd/homeshell'], {
      cwd: daemonDir,
    });
  });

  test.afterAll(() => {
    // Safety net: if a step failed before `stop`, don't leak the daemon.
    if (daemon && daemon.exitCode === null) daemon.kill('SIGKILL');
  });

  test('publishes a state file, refuses a second instance, and stops cleanly', async () => {
    // Nothing running yet.
    const before = await runCli(['status', `-home-dir=${homeDir}`]);
    expect(before.code).toBe(1);
    expect(before.stdout).toContain('not running');

    // Start a daemon on an ephemeral port, capturing stdout to check its
    // start-up report.
    daemon = spawn(
      binary,
      ['serve', '-demo=false', `-home-dir=${homeDir}`, '-addr', '127.0.0.1:0'],
      {
        cwd: daemonDir,
        stdio: ['ignore', 'pipe', 'ignore'],
      },
    );
    let serveStdout = '';
    daemon.stdout?.on('data', (chunk: Buffer) => {
      serveStdout += chunk.toString();
    });

    // It writes address, PID, version, and started-at after binding.
    expect(await waitFor(() => existsSync(stateFile), 30_000)).toBe(true);
    const state = readState();
    expect(state.address).toMatch(/^127\.0\.0\.1:\d+$/);
    expect(state.pid).toBe(daemon.pid);
    expect(typeof state.version).toBe('string');
    expect(Date.parse(state.startedAt)).not.toBeNaN();

    // On start-up it echoes the same report `status` gives, so its address and
    // state file are visible without a second command (PR #38 review).
    expect(await waitFor(() => serveStdout.includes('running'), 10_000)).toBe(
      true,
    );
    expect(serveStdout).toContain(state.address);
    expect(serveStdout).toContain(stateFile);

    // A client reaches the API through the discovered address, not a fixed port,
    // authenticating with the per-startup token published in the state file.
    expect(state.token).toBeTruthy();
    const hello = await fetch(`http://${state.address}/api/hello`, {
      headers: { Authorization: `Bearer ${state.token}` },
    });
    expect(hello.status).toBe(200);
    const helloBody = (await hello.json()) as { message: string };
    expect(helloBody.message).toContain('HomeShell');

    // `status` reports the running daemon.
    const running = await runCli(['status', `-home-dir=${homeDir}`]);
    expect(running.code).toBe(0);
    expect(running.stdout).toContain('running');
    expect(running.stdout).toContain(String(state.pid));
    expect(running.stdout).toContain(stateFile);

    // A second instance refuses to start and points at the running one.
    const second = await runCli([
      'serve',
      '-demo=false',
      `-home-dir=${homeDir}`,
      '-addr',
      '127.0.0.1:0',
    ]);
    expect(second.code).not.toBe(0);
    expect(second.stderr).toContain('already running');

    // `stop` drains the daemon and removes the state file.
    const stopped = await runCli(['stop', `-home-dir=${homeDir}`]);
    expect(stopped.code).toBe(0);
    expect(await waitFor(() => !existsSync(stateFile), 15_000)).toBe(true);

    // The port no longer answers, and status agrees.
    await expect(fetch(`http://${state.address}/api/hello`)).rejects.toThrow();
    const after = await runCli(['status', `-home-dir=${homeDir}`]);
    expect(after.code).toBe(1);
    expect(after.stdout).toContain('not running');
  });
});
