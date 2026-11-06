import { mkdir, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { type Page } from '@playwright/test';

/**
 * A cross-worker mutex for the settings document. Workers are separate
 * processes, so the lock is a directory in the temp dir — mkdir is atomic.
 */

const LOCK_DIR = join(tmpdir(), 'hs-e2e-settings-lock');

/** Past this age the lock is a crashed worker's leftover and is stolen. */
const STALE_MS = 60_000;

/** Runs `fn` while holding the settings lock; every settings write needs it. */
export async function withSettingsLock<T>(fn: () => Promise<T>): Promise<T> {
  const deadline = Date.now() + 120_000;
  for (;;) {
    try {
      await mkdir(LOCK_DIR);
      break;
    } catch {
      const age = await stat(LOCK_DIR)
        .then((s) => Date.now() - s.mtimeMs)
        .catch(() => 0);
      if (age > STALE_MS) {
        await rm(LOCK_DIR, { recursive: true, force: true });
        continue;
      }
      if (Date.now() > deadline) {
        throw new Error('timed out waiting for the settings lock');
      }
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
  }
  try {
    return await fn();
  } finally {
    await rm(LOCK_DIR, { recursive: true, force: true });
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
