import { expect, test } from '../fixtures';
import { addWorktree, tempGitRepo } from '../git-repo';
import { tabUntilFocused } from './keyboard-helpers';
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
  await selectProjectWithDefault(page, request, dir);
  const separate = page.getByRole('region', { name: /^Separate folders/ });
  const fold = separate.getByRole('button', { name: '1 untracked worktree' });
  await expect(fold).toBeVisible({ timeout: SUGGESTION_TIMEOUT });
  return { path, separate, fold };
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

test('the fold and Add are reachable by keyboard', async ({
  page,
  request,
}) => {
  const { fold } = await projectWithUntracked(page, request, 'wt-kbd');

  await page.getByRole('button', { name: 'New workstream' }).focus();
  await tabUntilFocused(page, fold);
  await page.keyboard.press('Enter');
  await expect(fold).toHaveAttribute('aria-expanded', 'true');

  const add = page.getByRole('button', { name: 'Add wt-kbd' });
  await tabUntilFocused(page, add, 10);
  await page.keyboard.press('Enter');

  await expect(
    page.getByRole('region', { name: 'Workstream wt-kbd' }),
  ).toBeVisible();
});
