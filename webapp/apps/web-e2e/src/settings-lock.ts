import { randomUUID } from 'node:crypto';
import { mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { type Page } from '@playwright/test';
import { settingsLockDir } from './e2e-env';

/**
 * A cross-worker mutex for the shared daemon's settings document. Workers are
 * separate processes, so the lock is a directory: it is built complete under
 * a private name, holding an `owner` file with the holder's pid, and acquired
 * by renaming it into place, which fails while another holder's lock exists.
 *
 * It lives under the checkout's e2e root, like the daemon it guards. A waiter
 * takes the lock over only from a holder whose process is gone (a worker
 * Playwright killed on a timeout); a live holder is waited for however long it
 * holds, up to the wait budget.
 */

const WAIT_MS = 120_000;

interface Owner {
  pid: number;
  token: string;
}

const isAlive = (pid: number) => {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    // EPERM: the pid exists but belongs to someone else.
    return (error as NodeJS.ErrnoException).code === 'EPERM';
  }
};

const readOwner = (dir: string): Promise<Owner | undefined> =>
  readFile(join(dir, 'owner'), 'utf8')
    .then((text) => JSON.parse(text) as Owner)
    .catch(() => undefined);

/** Tries once to acquire the lock; true when this call now holds it. */
async function tryAcquire(owner: Owner): Promise<boolean> {
  const staging = `${settingsLockDir}.${owner.token}`;
  await mkdir(dirname(settingsLockDir), { recursive: true });
  await mkdir(staging);
  await writeFile(join(staging, 'owner'), JSON.stringify(owner));
  try {
    // Renaming onto an existing, non-empty directory fails, so this is the
    // atomic acquire. Every lock directory holds its `owner` file.
    await rename(staging, settingsLockDir);
    return true;
  } catch {
    await rm(staging, { recursive: true, force: true });
    return false;
  }
}

/**
 * Removes the lock if its holder's process is gone. Moves it aside first and
 * checks it is still the dead holder's, so a lock a second waiter took over in
 * the meantime is put back rather than deleted.
 */
async function takeOverFromDeadHolder(): Promise<void> {
  const holder = await readOwner(settingsLockDir);
  if (!holder || isAlive(holder.pid)) return;
  const aside = `${settingsLockDir}.dead-${randomUUID()}`;
  try {
    await rename(settingsLockDir, aside);
  } catch {
    return; // Released or taken over already.
  }
  const moved = await readOwner(aside);
  if (moved?.token === holder.token) {
    await rm(aside, { recursive: true, force: true });
  } else {
    await rename(aside, settingsLockDir).catch(() => undefined);
  }
}

/** Runs `fn` while holding the settings lock; every settings write needs it. */
export async function withSettingsLock<T>(fn: () => Promise<T>): Promise<T> {
  const owner: Owner = { pid: process.pid, token: randomUUID() };
  const deadline = Date.now() + WAIT_MS;
  while (!(await tryAcquire(owner))) {
    await takeOverFromDeadHolder();
    if (Date.now() > deadline) {
      const holder = await readOwner(settingsLockDir);
      throw new Error(
        `timed out waiting for the settings lock (held by pid ${holder?.pid ?? 'unknown'})`,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  try {
    return await fn();
  } finally {
    // Only our own lock: never one a waiter took over after we were presumed
    // dead. Moved aside before deleting: `rm` empties the directory first, and
    // a waiter's rename succeeds onto an empty one.
    if ((await readOwner(settingsLockDir))?.token === owner.token) {
      const released = `${settingsLockDir}.released-${owner.token}`;
      await rename(settingsLockDir, released);
      await rm(released, { recursive: true, force: true });
    }
  }
}

/**
 * The next successful write of the settings document, or the next one whose
 * body satisfies `carries`: writes are debounced, so an earlier one may hold
 * only part of an edit.
 */
export const settingsSaved = <Settings = unknown>(
  page: Page,
  carries: (settings: Settings) => boolean = () => true,
) =>
  page.waitForResponse((res) => {
    if (
      !res.url().includes('/api/settings') ||
      res.request().method() !== 'PUT' ||
      !res.ok()
    ) {
      return false;
    }
    try {
      return carries(JSON.parse(res.request().postData() ?? ''));
    } catch {
      return false;
    }
  });
