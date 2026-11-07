import { execFile, execFileSync, spawn } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { promisify } from 'node:util';

/**
 * A non-demo daemon of a spec's own, on an ephemeral port with an isolated
 * `-home-dir`, independent of the daemon playwright.config.ts starts. For specs
 * that have to stop and start the daemon (persistence across a restart), which
 * the shared one cannot be asked to do. This is a helper module, not a spec:
 * the api project's testMatch (`api/*.spec.ts`) skips it.
 */

const execFileAsync = promisify(execFile);
const daemonDir = resolve(__dirname, '../../../../daemon');

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
  token: string;
}

export interface IsolatedDaemon {
  /** The data directory every file of this daemon lives in. */
  homeDir: string;
  /** Starts the daemon and waits until it publishes its address. */
  start(): Promise<void>;
  /** Stops it through `homeshell stop`, the way a user would. */
  stop(): Promise<void>;
  /** Kills it with SIGKILL, the way a crash would. */
  kill(): Promise<void>;
  /** Kills it if a step failed before `stop`, so no daemon leaks. */
  dispose(): void;
  /** An authenticated request against the running daemon. */
  api(path: string, init?: RequestInit): Promise<Response>;
}

/** Builds the daemon binary once per spec file, into a file named after it. */
export function buildDaemon(name: string, ldflags = ''): string {
  const binary = join(tmpdir(), `homeshell-${name}-e2e`);
  execFileSync(
    'go',
    ['build', '-ldflags', ldflags, '-o', binary, './cmd/homeshell'],
    { cwd: daemonDir },
  );
  return binary;
}

export function isolatedDaemon(
  binary: string,
  name: string,
  env: NodeJS.ProcessEnv = {},
): IsolatedDaemon {
  const homeDir = mkdtempSync(join(tmpdir(), `homeshell-${name}-`));
  const stateFile = join(homeDir, 'daemon.json');
  let child: ReturnType<typeof spawn> | undefined;
  let state: DaemonState | undefined;

  return {
    homeDir,
    async start() {
      child = spawn(
        binary,
        ['serve', '-demo=false', `-home-dir=${homeDir}`, '-addr', '127.0.0.1:0'],
        { cwd: daemonDir, stdio: 'ignore', env: { ...process.env, ...env } },
      );
      if (!(await waitFor(() => existsSync(stateFile), 30_000))) {
        throw new Error('the daemon did not publish its state file');
      }
      state = JSON.parse(readFileSync(stateFile, 'utf8')) as DaemonState;
    },
    async stop() {
      await execFileAsync(binary, ['stop', `-home-dir=${homeDir}`]);
      if (!(await waitFor(() => !existsSync(stateFile), 15_000))) {
        throw new Error('the daemon did not remove its state file on stop');
      }
      child = undefined;
      state = undefined;
    },
    async kill() {
      const running = child;
      if (!running) throw new Error('the daemon is not running');
      if (running.exitCode === null) {
        const exited = new Promise((r) => running.once('exit', r));
        running.kill('SIGKILL');
        await exited;
      }
      // A killed daemon leaves its address file behind; start() waits for a fresh one.
      rmSync(stateFile, { force: true });
      child = undefined;
      state = undefined;
    },
    dispose() {
      if (child && child.exitCode === null) child.kill('SIGKILL');
    },
    api(path, init = {}) {
      if (!state) throw new Error('the daemon is not running');
      return fetch(`http://${state.address}${path}`, {
        ...init,
        headers: {
          ...(init.headers ?? {}),
          Authorization: `Bearer ${state.token}`,
          'Content-Type': 'application/json',
        },
      });
    },
  };
}
