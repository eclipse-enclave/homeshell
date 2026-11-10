import { execFileSync } from 'node:child_process';
import { mkdirSync, statSync } from 'node:fs';
import { dirname } from 'node:path';
import { daemonDir, homeshellBin, scratchRoot } from './e2e-env';

/**
 * Runs after both web servers are up. The daemon's web server command made the
 * run's daemon build; a reused daemon skipped that command, so the build is
 * made here when it is missing or older than this run.
 */
export default function globalSetup(): void {
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
