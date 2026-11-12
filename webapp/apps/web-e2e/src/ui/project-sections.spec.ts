import { type Page } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { tempGitRepo } from '../git-repo';
import {
  openNewWorkstreamForm,
  selectProjectWithDefault,
  waitForRealCard,
} from './workstream-helpers';

/**
 * The project page in two sections: what works in the project folder, and
 * the separate folders (worktrees, copies). The project folder holds one
 * workstream, so the form offers "In place" only while it is free.
 */

/** Opens the form on the core provider; returns its strategy choices. */
async function strategiesOffered(page: Page) {
  const form = await openNewWorkstreamForm(page);
  await form.getByRole('radio', { name: 'Default' }).click();
  const group = form.getByRole('radiogroup', { name: 'Filesystem strategy' });
  await expect(group).toBeVisible();
  const offered = await group.getByRole('radio').allInnerTexts();
  await form.getByRole('button', { name: 'Cancel' }).click();
  return offered.map((label) => label.trim());
}

test('a git project shows its folder apart from its worktrees, and Work here takes the folder back', async ({
  page,
  request,
}) => {
  const dir = await tempGitRepo('hs-ui-sections-');
  const { projectId, workstream } = await selectProjectWithDefault(
    page,
    request,
    dir,
  );
  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'core', name: 'topic', config: {} },
  });
  expect(created.status()).toBe(201);

  const folder = page.getByRole('region', { name: 'In the project folder' });
  await expect(
    folder.getByRole('region', { name: 'Workstream feature' }),
  ).toBeVisible();
  await waitForRealCard(
    page
      .getByRole('region', { name: 'Separate folders · 1' })
      .getByRole('region', { name: 'Workstream topic' }),
  );
  expect(await strategiesOffered(page)).toEqual([
    'Worktree',
    'Folder: a copy of the project',
  ]);

  const removed = await request.delete(
    `/api/projects/${projectId}/workstreams/${workstream.id}`,
  );
  expect(removed.status()).toBe(204);
  await expect(
    folder.getByText('Nothing works in the project folder.'),
  ).toBeVisible();
  await folder.getByRole('button', { name: 'Work here' }).click();

  await waitForRealCard(
    folder.getByRole('region', { name: 'Workstream feature' }),
  );
  expect(await strategiesOffered(page)).not.toContain('In place');
});

test('outside git the separate folders are copies', async ({
  page,
  request,
}) => {
  await selectProjectWithDefault(
    page,
    request,
    await tempProjectDir('hs-ui-sections-plain-'),
  );

  await expect(
    page
      .getByRole('region', { name: 'Separate folders' })
      .getByText('No separate folders yet.'),
  ).toBeVisible();
  expect(await strategiesOffered(page)).toEqual([
    'Folder: a copy of the project',
  ]);
});
