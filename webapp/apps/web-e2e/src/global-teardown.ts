import { execFileSync } from 'node:child_process';
import { existsSync, readdirSync, rmSync } from 'node:fs';
import { basename, join } from 'node:path';
import { e2eRoot, scratchRoot, sharedRuntimeDir } from './e2e-env';
import { stopBridges } from './isolated-daemon';

/**
 * Runs while both web servers are still up, after every worker has finished.
 *
 * 1. Stops the shared daemon's bridges: they outlive the daemon by design, and
 *    Playwright stopping the daemon would leave them running.
 * 2. Leak guard: any process still referring to an isolated daemon's files is
 *    one a spec failed to tear down (see `disposeIsolatedHome`). They are
 *    killed, and the run fails naming them.
 * 3. Deletes every isolated home and fixture dir.
 */
export default async function globalTeardown(): Promise<void> {
  await stopBridges(sharedRuntimeDir);
  const leaked = leftoverProcesses([scratchRoot, sharedRuntimeDir]);
  if (leaked.length) await kill(leaked.map((p) => p.pid));
  if (existsSync(scratchRoot)) {
    for (const entry of readdirSync(scratchRoot)) {
      rmSync(join(scratchRoot, entry), { recursive: true, force: true });
    }
  }
  if (leaked.length) {
    const list = leaked.map((p) => `  ${p.pid} ${p.args}`).join('\n');
    console.error(`Leaked e2e processes, killed:\n${list}`);
    throw new Error(
      `${leaked.length} process(es) outlived their spec's teardown and were killed:\n${list}`,
    );
  }
}

interface Proc {
  pid: number;
  args: string;
}

const interpreters = new Set(['sh', 'bash', 'dash', 'node']);

/**
 * Processes the run started that still refer to one of `paths`: a program
 * under the e2e root (the daemon build: bridges, isolated daemons), or a
 * script under it run by a shell or node (scripted agents, Enclave fixtures).
 * The shared daemon refers to neither path. Anything else that merely mentions
 * a path (a developer's `tail -f`) is not ours to kill.
 */
function leftoverProcesses(paths: string[]): Proc[] {
  const listing = execFileSync('ps', ['-axww', '-o', 'pid=,args='], {
    encoding: 'utf8',
  });
  const underRoot = (arg = '') => arg.startsWith(`${e2eRoot}/`);
  return listing
    .split('\n')
    .map((line) => line.trim().match(/^(\d+)\s+(.*)$/))
    .filter((m): m is RegExpMatchArray => m !== null)
    .map((m) => ({ pid: Number(m[1]), args: m[2] }))
    .filter((p) => {
      const [program, script] = p.args.split(/\s+/);
      const started =
        underRoot(program) ||
        (interpreters.has(basename(program)) && underRoot(script));
      return (
        p.pid !== process.pid &&
        started &&
        paths.some((path) => p.args.includes(path))
      );
    });
}

const isAlive = (pid: number) => {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
};

/** SIGTERM, then SIGKILL whatever is still alive after 2 s. */
async function kill(pids: number[]): Promise<void> {
  const signal = (pid: number, sig: NodeJS.Signals) => {
    try {
      process.kill(pid, sig);
    } catch {
      // Already gone.
    }
  };
  pids.forEach((pid) => signal(pid, 'SIGTERM'));
  const deadline = Date.now() + 2_000;
  while (Date.now() < deadline && pids.some(isAlive)) {
    await new Promise((r) => setTimeout(r, 50));
  }
  pids.filter(isAlive).forEach((pid) => signal(pid, 'SIGKILL'));
}
