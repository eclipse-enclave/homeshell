import { execFile, execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { existsSync, readdirSync, readFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { isAlive } from './dir-lock';
import { daemonDir, e2eRoot, homeshellBin, scratchDir } from './e2e-env';

/**
 * Daemons of a spec's own, independent of the shared one playwright.config.ts
 * starts, and the one teardown every such daemon goes through. This is a
 * helper module, not a spec: the api project's testMatch skips it.
 */

const execFileAsync = promisify(execFile);

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

/** The longest unix socket path: `sun_path` has 108 bytes, its NUL included. */
export const MAX_SOCKET_PATH = 107;

/**
 * The longest bridge socket a daemon with `runtimeDir` as its
 * `XDG_RUNTIME_DIR` creates: `<dir>/homeshell-<12 hex>/hs-<uuid>.sock`, a
 * local session's (`hostbridge.New` in the daemon).
 */
export const worstSocketPath = (runtimeDir: string) =>
  join(runtimeDir, `homeshell-${'0'.repeat(12)}`, `hs-${randomUUID()}.sock`);

/**
 * Throws when a bridge under `runtimeDir` could not bind its socket, so a run
 * under a long `TMPDIR` fails here, naming the cause, rather than in a bridge.
 */
export function checkSocketBudget(runtimeDir: string): void {
  const path = worstSocketPath(runtimeDir);
  const length = Buffer.byteLength(path);
  if (length <= MAX_SOCKET_PATH) return;
  throw new Error(
    `bridge sockets under ${runtimeDir} would be ${length} bytes (${path}), past the ` +
      `${MAX_SOCKET_PATH} a unix socket path allows: use a shorter TMPDIR or HOMESHELL_E2E_ROOT`,
  );
}

/**
 * A home dir for an isolated daemon under the run's root. It is also the
 * daemon's runtime dir (`XDG_RUNTIME_DIR`), where its bridges' sockets go:
 * pass `runtimeEnv(home)` to the daemon so `disposeIsolatedHome` finds every
 * bridge it started. `prefix` has at most 4 chars, which keeps the sockets
 * within budget under a `TMPDIR` of up to 11 bytes (`/var/tmp` has 8).
 */
export function isolatedHome(prefix: string): string {
  if (prefix.length > 4) {
    throw new Error(`isolated home prefix "${prefix}" has at most 4 chars (socket path budget)`);
  }
  const home = scratchDir(`${prefix}-`);
  try {
    checkSocketBudget(home);
  } catch (err) {
    rmSync(home, { recursive: true, force: true });
    throw err;
  }
  return home;
}

export const runtimeEnv = (home: string) => ({ XDG_RUNTIME_DIR: home });

/** The bridge sockets under a runtime dir: `<dir>/homeshell-<hash>/<target>.sock`. */
export function bridgeSockets(runtimeDir: string): string[] {
  if (!existsSync(runtimeDir)) return [];
  return readdirSync(runtimeDir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && entry.name.startsWith('homeshell-'))
    .flatMap((entry) => {
      const dir = join(runtimeDir, entry.name);
      return readdirSync(dir)
        .filter((name) => name.endsWith('.sock'))
        .map((name) => join(dir, name));
    });
}

/**
 * Stops every bridge with a socket under `runtimeDir`, in parallel. Bridges
 * outlive their daemon by design, so nothing else ever stops them.
 */
export async function stopBridges(runtimeDir: string): Promise<void> {
  await Promise.all(
    bridgeSockets(runtimeDir).map((sock) =>
      execFileAsync(homeshellBin, ['bridge', 'stop', '--sock', sock, '--timeout', '5s'], {
        timeout: 10_000,
      }).catch(() => undefined),
    ),
  );
}

/**
 * The teardown of every isolated daemon: stop its bridges, stop the daemon
 * (SIGKILL if it is still up after 10 s), delete its home. Each step runs even
 * if the one before it failed. Bridges go first: once the daemon is gone
 * nothing stops them, and a live bridge re-creates its socket dir after `rm`.
 *
 * `child` is the daemon's process when the spec spawned it; without one the
 * pid is read from the home's `daemon.json` (a daemon `ensure` spawned).
 */
export async function disposeIsolatedHome(
  home: string,
  child?: ChildProcess,
): Promise<void> {
  try {
    await stopBridges(home);
  } finally {
    try {
      await stopDaemonAt(home, child);
    } finally {
      rmSync(home, { recursive: true, force: true });
    }
  }
}

/** Homes whose teardown has not run yet, keyed by home dir. */
const live = new Map<string, () => Promise<void>>();

/**
 * Registers an isolated daemon's home for teardown and returns its idempotent
 * dispose. `child` is read when dispose runs, so a restarted daemon's latest
 * process is the one stopped.
 */
export function trackIsolatedHome(
  home: string,
  child: () => ChildProcess | undefined,
): () => Promise<void> {
  let done: Promise<void> | undefined;
  const dispose = () => {
    done ??= disposeIsolatedHome(home, child()).finally(() => live.delete(home));
    return done;
  };
  live.set(home, dispose);
  return dispose;
}

/**
 * Disposes every isolated daemon this worker has not torn down yet. Register
 * it with `test.afterEach`/`test.afterAll` in a spec that starts isolated
 * daemons: those hooks still run when a test times out, its `finally` does not.
 */
export async function disposeIsolatedDaemons(): Promise<void> {
  await Promise.all([...live.values()].map((dispose) => dispose()));
}

async function stopDaemonAt(home: string, child?: ChildProcess): Promise<void> {
  const stateFile = join(home, 'daemon.json');
  let pid = child && child.exitCode === null && child.signalCode === null ? child.pid : undefined;
  if (pid === undefined && existsSync(stateFile)) {
    try {
      pid = (JSON.parse(readFileSync(stateFile, 'utf8')) as { pid?: number }).pid;
    } catch {
      // A half-written state file: nothing to stop by pid.
    }
  }
  if (pid === undefined || !isAlive(pid)) return;
  await execFileAsync(homeshellBin, ['stop', `-home-dir=${home}`], {
    timeout: 15_000,
  }).catch(() => undefined);
  const alive = () =>
    child ? child.exitCode === null && child.signalCode === null : isAlive(pid);
  if (await waitFor(() => !alive(), 10_000)) return;
  try {
    process.kill(pid, 'SIGKILL');
  } catch {
    return; // Gone meanwhile, or a reused pid that is not ours (EPERM).
  }
  await waitFor(() => !alive(), 5_000);
}

/**
 * A daemon build of a spec's own, for the specs `homeshellBin` cannot serve:
 * one stamped with `ldflags` (a version or channel a source build lacks), or
 * one built after embedding a UI the run's build lacked. Every other spec uses
 * the run's one build.
 */
export function buildDaemonVariant(name: string, ldflags = ''): string {
  const binary = join(e2eRoot, 'bin', `homeshell-${name}`);
  execFileSync('go', ['build', '-ldflags', ldflags, '-o', binary, './cmd/homeshell'], {
    cwd: daemonDir,
  });
  return binary;
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
  /** The shared teardown (`disposeIsolatedHome`): bridges, daemon, home. */
  dispose(): Promise<void>;
  /** An authenticated request against the running daemon. */
  api(path: string, init?: RequestInit): Promise<Response>;
}

/**
 * A non-demo daemon on an ephemeral port with an isolated `-home-dir`, for
 * specs that have to stop and start the daemon (persistence across a
 * restart), which the shared one cannot be asked to do.
 */
export function isolatedDaemon(
  name: string,
  { binary = homeshellBin, env = {} }: { binary?: string; env?: NodeJS.ProcessEnv } = {},
): IsolatedDaemon {
  const homeDir = isolatedHome(name);
  const stateFile = join(homeDir, 'daemon.json');
  let child: ChildProcess | undefined;
  let state: DaemonState | undefined;
  const dispose = trackIsolatedHome(homeDir, () => child);

  return {
    homeDir,
    async start() {
      child = spawn(
        binary,
        ['serve', '-demo=false', `-home-dir=${homeDir}`, '-addr', '127.0.0.1:0'],
        {
          cwd: daemonDir,
          stdio: 'ignore',
          env: { ...process.env, ...runtimeEnv(homeDir), ...env },
        },
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
    dispose,
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
