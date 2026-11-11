import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, join } from 'node:path';
import { test as base, type APIRequestContext } from '@playwright/test';
import { retryOnNetworkChange } from './page-load-retry';

/**
 * Shared Playwright fixtures. Import `test`/`expect` from here instead of
 * `@playwright/test` to get them.
 */

/**
 * Directories handed out to the running test, drained by the fixture below.
 * Module scope keeps the plain-function helpers in the specs signature-free;
 * a worker runs one test at a time, so this only holds that test's directories.
 */
const pending: string[] = [];

/** Creates a throwaway project directory, removed with its project after the test. */
export async function tempProjectDir(prefix: string): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), prefix));
  pending.push(dir);
  return dir;
}

/**
 * Removes the projects at `dirs`. Matched on the basename: the daemon stores
 * the path it resolved, and mkdtemp's suffix already makes the basename unique.
 */
async function removeProjectsAt(
  request: APIRequestContext,
  dirs: string[],
): Promise<void> {
  const wanted = new Set(dirs.map((dir) => basename(dir)));
  const res = await request.get('/api/projects');
  if (!res.ok()) return;
  const { projects = [] } = (await res.json()) as {
    projects?: { id: string; path: string }[];
  };
  for (const project of projects) {
    if (wanted.has(basename(project.path))) {
      await request.delete(`/api/projects/${project.id}`);
    }
  }
}

export const test = base.extend<{ throwawayProjects: void }>({
  // goto and reload load the page again when a host network change broke the
  // load, or once when it timed out (src/page-load-retry.ts); each retry is a
  // test annotation.
  page: async ({ page }, use, testInfo) => {
    const opts = {
      annotate: (type: string, description: string) =>
        testInfo.annotations.push({ type, description }),
    };
    const goto = page.goto.bind(page);
    const reload = page.reload.bind(page);
    page.goto = (url, options) =>
      retryOnNetworkChange(
        page,
        (timeout) => goto(url, { ...options, timeout: options?.timeout ?? timeout }),
        opts,
      );
    page.reload = (options) =>
      retryOnNetworkChange(
        page,
        (timeout) => reload({ ...options, timeout: options?.timeout ?? timeout }),
        opts,
      );
    await use(page);
  },
  // Only what this test created: the other workers' projects are in the same
  // list, so a broader rule would pull rows out from under a parallel test.
  throwawayProjects: [
    async ({ request }, use) => {
      await use();
      const dirs = pending.splice(0);
      if (dirs.length === 0) return;
      await removeProjectsAt(request, dirs);
      await Promise.all(
        dirs.map((dir) => rm(dir, { recursive: true, force: true })),
      );
    },
    { auto: true },
  ],
});

export { expect } from '@playwright/test';
