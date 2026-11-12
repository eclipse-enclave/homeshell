import { mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';

/**
 * A lock between processes, as a directory: it is built complete under a
 * private name, holding an `owner` file with the holder's pid, and acquired by
 * renaming it into place, which fails while another holder's lock exists.
 * A holder whose process is gone (a worker Playwright killed, a run stopped
 * with Ctrl-C) can be taken over. Used by the settings lock and the run lock.
 */

export interface Owner {
  pid: number;
  token: string;
}

/** Whether `pid` runs. EPERM: it exists but belongs to someone else. */
export function isAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === 'EPERM';
  }
}

export const readOwner = (dir: string): Promise<Owner | undefined> =>
  readFile(join(dir, 'owner'), 'utf8')
    .then((text) => JSON.parse(text) as Owner)
    .catch(() => undefined);

/** Tries once to acquire the lock at `dir`; true when `owner` now holds it. */
export async function tryAcquire(dir: string, owner: Owner): Promise<boolean> {
  const staging = `${dir}.${owner.token}`;
  await mkdir(dirname(dir), { recursive: true });
  await mkdir(staging);
  await writeFile(join(staging, 'owner'), JSON.stringify(owner));
  try {
    // Renaming onto an existing, non-empty directory fails, so this is the
    // atomic acquire. Every lock directory holds its `owner` file.
    await rename(staging, dir);
    return true;
  } catch {
    await rm(staging, { recursive: true, force: true });
    return false;
  }
}

/**
 * Removes the lock at `dir` of `holder`, a holder whose process is gone, for a
 * waiter that read it from the lock earlier: by now another waiter may have
 * removed it and a third acquired.
 *
 * The waiter first claims the dead lock with a `reap-<token>` dir inside it,
 * then reads the owner again. Reading the dead holder's owner there proves the
 * claim landed in the dead lock: that lock was there before the claim and
 * nothing replaces a lock while it is in place. Only one claim succeeds, and
 * the dead lock cannot change under its claimant, so it is removed outright.
 * Why not move it aside and check: putting back a live lock moved aside by
 * mistake leaves the slot empty meanwhile, and a second worker acquires.
 *
 * A claim that landed in a newer lock is left there: its name is keyed to the
 * dead holder, so it blocks no one, and goes when that lock is released.
 */
export async function takeOverFrom(dir: string, holder: Owner): Promise<void> {
  try {
    await mkdir(join(dir, `reap-${holder.token}`));
  } catch {
    return; // Released, removed, or another waiter's claim.
  }
  if ((await readOwner(dir))?.token !== holder.token) return;
  const dead = `${dir}.dead-${holder.token}`;
  await rename(dir, dead);
  await rm(dead, { recursive: true, force: true });
}

/**
 * Releases the lock at `dir` if `owner` holds it: never one a waiter took over
 * after `owner` was presumed dead. Moved aside before deleting: `rm` empties
 * the directory first, and a waiter's rename succeeds onto an empty one.
 */
export async function release(dir: string, owner: Owner): Promise<void> {
  if ((await readOwner(dir))?.token !== owner.token) return;
  const released = `${dir}.released-${owner.token}`;
  await rename(dir, released);
  await rm(released, { recursive: true, force: true });
}
