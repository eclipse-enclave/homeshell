import { expect, test } from '../fixtures';
import { createWorkstream } from '../api/workstream-helpers';
import { loadApp } from './keyboard-helpers';
import {
  materializedWorkstream,
  selectProject,
  unique,
} from './workstream-helpers';

/**
 * The provider pill on a workstream card: the provider's display name, and none
 * at all for the built-in provider, whose strategy pill already says it all.
 */
test('names a non-default provider and leaves the built-in one unmarked', async ({
  page,
  request,
}) => {
  const { dirName, projectId, workstreamName } = await materializedWorkstream(
    request,
    'hs-ui-provider-',
    'built-in',
  );
  const fakeName = unique('faked');
  await createWorkstream(request, projectId, fakeName);

  await loadApp(page);
  await selectProject(page, dirName);

  const fakeCard = page.getByRole('region', { name: `Workstream ${fakeName}` });
  await expect(
    fakeCard.getByText('Fake Provider', { exact: true }),
  ).toBeVisible();
  const coreCard = page.getByRole('region', {
    name: `Workstream ${workstreamName}`,
  });
  await expect(coreCard.getByText('in place', { exact: true })).toBeVisible();
  // The built-in provider's display name.
  await expect(coreCard.getByText('Default', { exact: true })).toHaveCount(0);
});
