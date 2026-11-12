import { expect, test } from '../fixtures';
import {
  addUntrackedWorktree,
  createSession,
} from '../api/workstream-helpers';
import { addWorktree, git, tempGitRepo } from '../git-repo';
import { selectProjectWithDefault } from './workstream-helpers';

/**
 * A worktree made outside HomeShell is offered in the project view, under
 * Separate folders. The daemon surveys on its refresh interval, so the fold
 * is waited for.
 */

const SUGGESTION_TIMEOUT = 15_000;

/** A project with a worktree on branch `branch` made outside HomeShell; the fold, awaited. */
async function projectWithUntracked(
  page: Parameters<typeof selectProjectWithDefault>[0],
  request: Parameters<typeof selectProjectWithDefault>[1],
  branch: string,
) {
  const dir = await tempGitRepo('hs-ui-untracked-');
  const path = await addWorktree(dir, { branch });
  const { projectId } = await selectProjectWithDefault(page, request, dir);
  const separate = page.getByRole('region', { name: /^Separate folders/ });
  const fold = separate.getByRole('button', { name: '1 untracked worktree' });
  await expect(fold).toBeVisible({ timeout: SUGGESTION_TIMEOUT });
  return { dir, path, projectId, separate, fold };
}

test('a worktree made outside HomeShell can be added from the project view', async ({
  page,
  request,
}) => {
  const { path, separate, fold } = await projectWithUntracked(
    page,
    request,
    'wt-ui',
  );

  await fold.click();
  const row = separate.getByRole('listitem').filter({ hasText: 'wt-ui' });
  await expect(row).toContainText(path);

  await row.getByRole('button', { name: 'Add wt-ui' }).click();

  await expect(
    page.getByRole('region', { name: 'Workstream wt-ui' }),
  ).toBeVisible();
  await expect(fold).toBeHidden();
});

test('the arrow keys reach the fold and its rows, and Enter adds one', async ({
  page,
  request,
}) => {
  const { fold } = await projectWithUntracked(page, request, 'wt-arrows');

  await page.getByRole('region', { name: /^Workstream / }).first().focus();
  await page.keyboard.press('ArrowDown');
  await expect(fold).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(fold).toHaveAttribute('aria-expanded', 'true');
  await page.keyboard.press('ArrowDown');
  const row = page.getByRole('listitem').filter({ hasText: 'wt-arrows' });
  await expect(row).toBeFocused();

  await page.keyboard.press('Enter');
  await expect(
    page.getByRole('region', { name: 'Workstream wt-arrows' }),
  ).toBeVisible();
});

test('Tab comes back to the row the arrows left, and on to its controls', async ({
  page,
  request,
}) => {
  await projectWithUntracked(page, request, 'wt-kbd');

  await page.getByRole('region', { name: /^Workstream / }).first().focus();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('ArrowRight');
  await page.keyboard.press('ArrowDown');
  const row = page.getByRole('listitem').filter({ hasText: 'wt-kbd' });
  await expect(row).toBeFocused();

  // The list keeps one tab stop: Tab from above lands on the row, not the fold.
  await page.getByRole('button', { name: 'New workstream' }).focus();
  await page.keyboard.press('Tab');
  await expect(row).toBeFocused();

  await page.keyboard.press('Tab');
  await expect(row.getByRole('button', { name: 'Copy path' })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('button', { name: 'Add wt-kbd' })).toBeFocused();
  await page.keyboard.press('Enter');

  await expect(
    page.getByRole('region', { name: 'Workstream wt-kbd' }),
  ).toBeVisible();
});

test('a worktree removed outside with a session running says its folder is gone', async ({
  page,
  request,
}) => {
  const { dir, path, projectId } = await projectWithUntracked(
    page,
    request,
    'wt-gone',
  );
  const added = await addUntrackedWorktree(request, projectId, path);
  expect(added.status(), await added.text()).toBe(201);
  await createSession(request, projectId, (await added.json()).id);
  const card = page.getByRole('region', { name: 'Workstream wt-gone' });
  await expect(card).toBeVisible();

  await git(dir, 'worktree', 'remove', '--force', path);

  await expect(
    card.getByText('Its folder is gone. It is removed once its sessions end.'),
  ).toBeVisible({ timeout: SUGGESTION_TIMEOUT });
});
