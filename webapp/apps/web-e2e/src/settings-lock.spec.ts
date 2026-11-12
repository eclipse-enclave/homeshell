import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import * as fsp from 'node:fs/promises';
import { mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

/**
 * The cross-worker settings lock, against a real directory: every worker is
 * modelled by what it leaves on disk, the same as Playwright's processes.
 */

const root = mkdtempSync(join(tmpdir(), 'hs-lock-'));
vi.stubEnv('HOMESHELL_E2E_ROOT', root);

/**
 * A worker that acquires the lock the moment its slot is empty, however
 * briefly: armed, it tries after every rename the code under test makes.
 */
const intruder = { armed: false, got: false };

vi.mock('node:fs/promises', async (importOriginal) => {
  const real = await importOriginal<typeof fsp>();
  const rename: typeof real.rename = async (from, to) => {
    await real.rename(from, to);
    if (intruder.armed && !intruder.got) {
      intruder.got = await handAcquire(real, { pid: process.pid, token: 'intruder' });
    }
  };
  return { ...real, rename, default: { ...real, rename } };
});

let lockDir: string;
let lock: typeof import('./settings-lock');
let dirLock: typeof import('./dir-lock');

beforeAll(async () => {
  lock = await import('./settings-lock');
  dirLock = await import('./dir-lock');
  lockDir = (await import('./e2e-env')).settingsLockDir;
});

afterEach(() => {
  intruder.armed = false;
  intruder.got = false;
  for (const entry of readdirSync(root)) rmSync(join(root, entry), { recursive: true, force: true });
});

afterAll(() => rmSync(root, { recursive: true, force: true }));

interface Owner {
  pid: number;
  token: string;
}

/** What a worker's acquire does: a complete lock dir renamed into place. */
async function handAcquire(fs: typeof fsp, owner: Owner): Promise<boolean> {
  const staging = `${lockDir}.hand-${randomUUID()}`;
  await fs.mkdir(staging, { recursive: true });
  await fs.writeFile(join(staging, 'owner'), JSON.stringify(owner));
  try {
    await fs.rename(staging, lockDir);
    return true;
  } catch {
    await fs.rm(staging, { recursive: true, force: true });
    return false;
  }
}

/** A pid whose process has exited. */
const deadPid = () => spawnSync('true').pid as number;

const holder = (): Owner | undefined => {
  try {
    return JSON.parse(readFileSync(join(lockDir, 'owner'), 'utf8')) as Owner;
  } catch {
    return undefined;
  }
};

const leftovers = () => readdirSync(root).filter((name) => name.startsWith('settings.lock'));

describe('withSettingsLock', () => {
  it('runs the callback, returns its result and leaves no lock behind', async () => {
    await expect(lock.withSettingsLock(async () => 'done')).resolves.toBe('done');
    expect(leftovers()).toEqual([]);
  });

  it('releases the lock when the callback throws', async () => {
    await expect(
      lock.withSettingsLock(async () => {
        throw new Error('boom');
      }),
    ).rejects.toThrow('boom');
    expect(leftovers()).toEqual([]);
  });

  it('never runs two callers at once', async () => {
    let inside = 0;
    let most = 0;
    await Promise.all(
      Array.from({ length: 8 }, () =>
        lock.withSettingsLock(async () => {
          most = Math.max(most, ++inside);
          await new Promise((r) => setTimeout(r, 5));
          inside--;
        }),
      ),
    );
    expect(most).toBe(1);
    expect(leftovers()).toEqual([]);
  });

  it('waits for a live holder', async () => {
    await handAcquire(fsp, { pid: process.pid, token: 'live' });
    let ran = false;
    const waiting = lock.withSettingsLock(async () => {
      ran = true;
    });
    await new Promise((r) => setTimeout(r, 200));
    expect(ran).toBe(false);

    const released = `${lockDir}.released-live`;
    await fsp.rename(lockDir, released);
    await fsp.rm(released, { recursive: true });
    await waiting;
    expect(ran).toBe(true);
  });

  it('takes the lock over from a holder whose process is gone', async () => {
    await handAcquire(fsp, { pid: deadPid(), token: 'dead' });
    await expect(lock.withSettingsLock(async () => 'mine')).resolves.toBe('mine');
    expect(leftovers()).toEqual([]);
  });
});

describe('takeOverFrom', () => {
  it('removes the lock of the dead holder it was given', async () => {
    const dead = { pid: deadPid(), token: 'dead' };
    await handAcquire(fsp, dead);
    await dirLock.takeOverFrom(lockDir, dead);
    expect(leftovers()).toEqual([]);
  });

  // A waiter saw the dead holder, but by the time it acts another waiter has
  // removed that lock and a third has acquired: the lock it now finds is live.
  it('leaves a lock taken since alone, and lets no one in meanwhile', async () => {
    const dead = { pid: deadPid(), token: 'dead' };
    await handAcquire(fsp, { pid: process.pid, token: 'current' });

    intruder.armed = true;
    await dirLock.takeOverFrom(lockDir, dead);
    intruder.armed = false;

    expect(intruder.got).toBe(false);
    expect(holder()?.token).toBe('current');
    expect(leftovers()).toEqual(['settings.lock']);
  });

  it('lets only one of several waiters remove the dead lock', async () => {
    const dead = { pid: deadPid(), token: 'dead' };
    await handAcquire(fsp, dead);

    intruder.armed = true;
    await Promise.all([1, 2, 3].map(() => dirLock.takeOverFrom(lockDir, dead)));
    intruder.armed = false;

    // The one removal empties the slot once, and the intruder takes it; no
    // second waiter then moves the intruder's lock aside.
    expect(intruder.got).toBe(true);
    expect(holder()?.token).toBe('intruder');
    expect(leftovers()).toEqual(['settings.lock']);
  });
});
