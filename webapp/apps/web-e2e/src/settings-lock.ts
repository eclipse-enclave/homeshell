import { randomUUID } from 'node:crypto';
import { type Page } from '@playwright/test';
import { isAlive, type Owner, readOwner, release, takeOverFrom, tryAcquire } from './dir-lock';
import { settingsLockDir } from './e2e-env';

/**
 * A cross-worker mutex for what the shared daemon saves in its settings file:
 * the settings document and the live log level. Workers are separate
 * processes, so it is a directory lock (dir-lock.ts).
 *
 * It lives under the checkout's e2e root, like the daemon it guards. A waiter
 * takes the lock over only from a holder whose process is gone (a worker
 * Playwright killed on a timeout); a live holder is waited for however long it
 * holds, up to the wait budget.
 */

const WAIT_MS = 120_000;

/** Runs `fn` while holding the settings lock; every settings write needs it. */
export async function withSettingsLock<T>(fn: () => Promise<T>): Promise<T> {
  const owner: Owner = { pid: process.pid, token: randomUUID() };
  const deadline = Date.now() + WAIT_MS;
  while (!(await tryAcquire(settingsLockDir, owner))) {
    const holder = await readOwner(settingsLockDir);
    if (holder && !isAlive(holder.pid)) await takeOverFrom(settingsLockDir, holder);
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
    await release(settingsLockDir, owner);
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
