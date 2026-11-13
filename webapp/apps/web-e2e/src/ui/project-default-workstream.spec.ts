import { basename } from 'node:path';
import { expect, test } from '../fixtures';
import { tempGitRepo } from '../git-repo';
import { withSettingsLock } from '../settings-lock';
import { loadApp } from './keyboard-helpers';
import {
  openNewWorkstreamForm,
  selectEmptyProject,
  selectProject,
  selectProjectWithDefault,
} from './workstream-helpers';

/**
 * A project comes with a workstream to start in, named after the branch
 * checked out in its folder; another one defaults to a git worktree.
 */

test('a git project added in the UI comes with a workstream named after its branch', async ({
  page,
}) => {
  const dir = await tempGitRepo('hs-ui-default-ws-');
  await loadApp(page);
  await page.getByRole('button', { name: 'Add project' }).click();
  await page.getByPlaceholder('/path/to/project').fill(dir);
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await selectProject(page, basename(dir));

  const card = page.getByRole('region', { name: 'Workstream feature' });
  await expect(card).toBeVisible();
  // Sessions start in it straight away.
  await expect(card.getByRole('button', { name: 'New session' })).toBeEnabled();
});

test('a new workstream in a git project starts on a worktree', async ({
  page,
  request,
}) => {
  const dir = await tempGitRepo('hs-ui-default-ws-form-');
  await withSettingsLock(async () => {
    await selectProjectWithDefault(page, request, dir);
    const form = await openNewWorkstreamForm(page);
    await form.getByRole('radio', { name: 'Default' }).click();
    await expect(form.getByRole('radio', { name: 'Worktree' })).toHaveAttribute(
      'aria-checked',
      'true',
    );
  });
});

test('a new workstream outside git starts in place while the folder is free', async ({
  page,
  request,
}) => {
  await withSettingsLock(async () => {
    await selectEmptyProject(page, request, 'hs-ui-default-ws-plain-');
    const form = await openNewWorkstreamForm(page);
    await form.getByRole('radio', { name: 'Default' }).click();
    await expect(form.getByRole('radio', { name: 'In place' })).toHaveAttribute(
      'aria-checked',
      'true',
    );
  });
});
