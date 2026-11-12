import { basename } from 'node:path';
import { expect, test } from '../fixtures';
import { awaitWorkstreamNamed } from '../api/workstream-helpers';
import { gitProject } from '../git-repo';
import { loadApp, readClipboard } from './keyboard-helpers';
import { selectProject, unique, waitForRealCard } from './workstream-helpers';

/**
 * A separate folder's card says on its second line how the folder was made and
 * where it is, and copies its full path.
 */
test('a worktree card shows its branch, kind and path, and copies the path', async ({
  page,
  context,
  request,
}) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const { dir, projectId } = await gitProject(request, 'hs-ui-header-');
  const name = unique('header');
  const branch = unique('topic');
  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      name,
      config: { strategy: 'worktree', branch },
    },
  });
  expect(created.status()).toBe(201);
  const workstream = await awaitWorkstreamNamed(request, projectId, name);

  await loadApp(page);
  await selectProject(page, basename(dir));
  const card = page.getByRole('region', { name: `Workstream ${name}` });
  await waitForRealCard(card);
  await expect(card.getByText(branch, { exact: true })).toBeVisible();
  await expect(card.getByText('worktree', { exact: true })).toBeVisible();
  // Not beside the project here, so shown in full.
  const workingDir = workstream.workingDir ?? '';
  await expect(card.getByText(workingDir, { exact: true })).toBeVisible();

  await card.getByRole('button', { name: 'Copy path' }).click();
  expect(await readClipboard(page)).toBe(workingDir);
});
