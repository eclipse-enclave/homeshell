import { spawnSync } from 'node:child_process';
import { mkdtempSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

/** One run per checkout: the run lock, against a real e2e root. */

const root = mkdtempSync(join(tmpdir(), 'hs-run-'));
vi.stubEnv('HOMESHELL_E2E_ROOT', root);

let run: typeof import('./run-lock');
let dirLock: typeof import('./dir-lock');

beforeAll(async () => {
  run = await import('./run-lock');
  dirLock = await import('./dir-lock');
});

afterEach(() => {
  for (const entry of readdirSync(root)) rmSync(join(root, entry), { recursive: true, force: true });
});

afterAll(() => rmSync(root, { recursive: true, force: true }));

/** A pid whose process has exited. */
const deadPid = () => spawnSync('true').pid as number;

const lockEntries = () => readdirSync(root).filter((name) => name.startsWith('run.lock'));

describe('run lock', () => {
  it('is acquired by a run, held until released, and leaves nothing behind', async () => {
    expect(await run.holdsRunLock()).toBe(false);
    await run.acquireRunLock();
    expect(await run.holdsRunLock()).toBe(true);
    await run.releaseRunLock();
    expect(await run.holdsRunLock()).toBe(false);
    expect(lockEntries()).toEqual([]);
  });

  it('refuses a second run while the first is alive, naming it', async () => {
    // The parent process: alive, and not this one.
    const first = { pid: process.ppid, token: 'first' };
    await dirLock.tryAcquire(run.runLockDir, first);

    await expect(run.acquireRunLock()).rejects.toThrow(
      new RegExp(`pid ${process.ppid}.*${root}.*worktree`, 's'),
    );
    expect(await run.holdsRunLock()).toBe(false);
    expect((await dirLock.readOwner(run.runLockDir))?.token).toBe('first');
  });

  it('takes the lock over from a run whose process is gone', async () => {
    await dirLock.tryAcquire(run.runLockDir, { pid: deadPid(), token: 'dead' });
    await run.acquireRunLock();
    expect(await run.holdsRunLock()).toBe(true);
    await run.releaseRunLock();
    expect(lockEntries()).toEqual([]);
  });

  it('is not released by a run that does not hold it', async () => {
    await dirLock.tryAcquire(run.runLockDir, { pid: process.ppid, token: 'first' });
    await run.releaseRunLock();
    expect((await dirLock.readOwner(run.runLockDir))?.token).toBe('first');
  });
});
