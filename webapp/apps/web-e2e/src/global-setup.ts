import { execFileSync } from 'node:child_process';
import { mkdirSync, statSync } from 'node:fs';
import { dirname } from 'node:path';
import { daemonDir, homeshellBin, scratchRoot } from './e2e-env';
import { acquireRunLock } from './run-lock';

/**
 * Runs after both web servers are up. Takes the run lock first (run-lock.ts):
 * a second run in this checkout fails here. The daemon's web server command
 * made the run's daemon build; a reused daemon skipped that command, so the
 * build is made here when it is missing or older than this run.
 */
export default async function globalSetup(): Promise<void> {
  await acquireRunLock();
  const runStart = Number(process.env.HOMESHELL_E2E_RUN_START);
  const builtAt = (() => {
    try {
      return statSync(homeshellBin).mtimeMs;
    } catch {
      return undefined;
    }
  })();
  if (builtAt === undefined || !(builtAt >= runStart)) {
    mkdirSync(dirname(homeshellBin), { recursive: true });
    execFileSync('go', ['build', '-o', homeshellBin, './cmd/homeshell'], {
      cwd: daemonDir,
      stdio: 'inherit',
    });
  }
  mkdirSync(scratchRoot, { recursive: true });
}
