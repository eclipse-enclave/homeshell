import { execFile } from 'node:child_process';
import { writeFile } from 'node:fs/promises';
import { basename, join } from 'node:path';
import { promisify } from 'node:util';
import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { selectProject } from './workstream-helpers';
import { addEmptyProject } from '../projects';
import { createWorkstream } from '../api/workstream-helpers';

const execFileAsync = promisify(execFile);

/**
 * The workstream card's status message and its dismiss (×) button: when the card
 * offers one, and when it must not. A session's own failure is not here — it
 * belongs to the session row (ui/session-error.spec.ts).
 */

async function projectWithWorkstream(
  request: APIRequestContext,
): Promise<{ dirName: string; projectId: string; workstreamId: string }> {
  const dir = await tempProjectDir('hs-ui-status-');
  const projectId = await addEmptyProject(request, dir);

  const workstreamId = await createWorkstream(request, projectId, 'ui-status');
  return { dirName: basename(dir), projectId, workstreamId };
}

async function openCard(page: Page, dirName: string) {
  await page.goto('/');
  await selectProject(page, dirName);
  const card = page.getByRole('region', { name: 'Workstream ui-status' });
  await expect(card).toBeVisible();
  return card;
}

/*
 * There is no longer a spec here for clicking the × on a *dismissible* message,
 * because nothing a browser can reach still writes one. A failed session create
 * used to, and now explains itself on its own session row instead
 * (ui/session-error.spec.ts); a failed workstream create writes the other kind,
 * which is deliberately not dismissible (below). What remains of the workstream
 * message is the fallback for a create whose placeholder vanished — not
 * reachable over HTTP — so the clear path is covered where it can be driven:
 * daemon/internal/api/workstreams_status_test.go.
 */

test('a workstream with no status message shows no dismiss button', async ({
  page,
  request,
}) => {
  const { dirName } = await projectWithWorkstream(request);
  const card = await openCard(page, dirName);

  await expect(
    card.getByRole('button', { name: /Dismiss message for workstream/ }),
  ).toHaveCount(0);
});

/**
 * A git repository with a `main` branch, so asking for a worktree on a new
 * `main/topic` is a creation that really fails: git keeps branches as files, and
 * `main` is one where `main/topic` would need a folder.
 */
async function failedWorkstream(
  request: APIRequestContext,
): Promise<{ dirName: string }> {
  const dir = await tempProjectDir('hs-ui-status-err-');
  const env = {
    ...process.env,
    GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_CONFIG_SYSTEM: '/dev/null',
    GIT_AUTHOR_NAME: 'e2e',
    GIT_AUTHOR_EMAIL: 'e2e@example.com',
    GIT_COMMITTER_NAME: 'e2e',
    GIT_COMMITTER_EMAIL: 'e2e@example.com',
  };
  await execFileAsync('git', ['init', '-q', '-b', 'main'], { cwd: dir, env });
  await writeFile(join(dir, 'README.md'), '# e2e\n');
  await execFileAsync('git', ['add', '.'], { cwd: dir, env });
  await execFileAsync('git', ['commit', '-qm', 'initial'], { cwd: dir, env });

  const projectId = await addEmptyProject(request, dir);

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      name: 'ui-dup-branch',
      config: { strategy: 'worktree', branch: 'main/topic' },
    },
  });
  expect(res.status()).toBe(201);
  return { dirName: basename(dir) };
}

// The regression: the × was offered on a failed creation too, and clicking it
// left a card that was permanently red with nothing left to say why.
test('a failed workstream shows its error with no way to dismiss it', async ({
  page,
  request,
}) => {
  const { dirName } = await failedWorkstream(request);

  await page.goto('/');
  await selectProject(page, dirName);
  const card = page.getByRole('region', { name: 'Workstream ui-dup-branch' });
  await expect(card).toBeVisible();

  // The failure is on screen, in git's words without its exit status…
  await expect(
    card.getByText(
      /^could not create the worktree: cannot lock ref 'refs\/heads\/main\/topic'/,
    ),
  ).toBeVisible();
  await expect(card.getByText(/exit status/)).toHaveCount(0);
  // …and there is no × to delete the only account of it.
  await expect(
    card.getByRole('button', { name: /Dismiss message for workstream/ }),
  ).toHaveCount(0);

  // The card is still disposable — that is the way out of a failed creation,
  // and all it offers: there is nothing to open, rename or start a session in.
  await expect(card.getByRole('button')).toHaveAccessibleName(
    'Delete workstream ui-dup-branch',
  );
});
