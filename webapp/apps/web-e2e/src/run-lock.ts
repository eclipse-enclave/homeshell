import { randomUUID } from 'node:crypto';
import { join } from 'node:path';
import { isAlive, readOwner, release, takeOverFrom, tryAcquire } from './dir-lock';
import { e2eRoot } from './e2e-env';

/**
 * One run per checkout. globalTeardown kills and deletes everything under the
 * e2e root, so a second run in the same checkout (a reused server makes that
 * easy) would break the first. globalSetup takes this lock, held by the
 * runner process, and globalTeardown acts only while it holds it: Playwright
 * runs globalTeardown even when globalSetup threw. A run stopped before its
 * teardown leaves the lock to a dead pid, and the next run takes it over.
 */

export const runLockDir = join(e2eRoot, 'run.lock');

export async function acquireRunLock(): Promise<void> {
  const owner = { pid: process.pid, token: randomUUID() };
  // A few rounds: a takeover can lose to another run starting at once, which
  // the next round then refuses.
  for (let round = 0; round < 5; round++) {
    if (await tryAcquire(runLockDir, owner)) return;
    const holder = await readOwner(runLockDir);
    if (holder && isAlive(holder.pid)) {
      throw new Error(
        `another e2e run (pid ${holder.pid}) is using this checkout's e2e root ${e2eRoot}. ` +
          'Wait for it to finish, or run the second one from another worktree.',
      );
    }
    if (holder) await takeOverFrom(runLockDir, holder);
  }
  throw new Error(`could not take the e2e run lock ${runLockDir}`);
}

/** Whether this process (the runner) holds the run lock. */
export async function holdsRunLock(): Promise<boolean> {
  return (await readOwner(runLockDir))?.pid === process.pid;
}

export async function releaseRunLock(): Promise<void> {
  const holder = await readOwner(runLockDir);
  if (holder?.pid === process.pid) await release(runLockDir, holder);
}
