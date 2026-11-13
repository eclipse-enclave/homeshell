import { basename } from 'node:path';

import { expect, tempProjectDir, test } from '../fixtures';
import { addEmptyProject } from '../projects';

/**
 * Labels that are cut off with an ellipsis when space runs out show their
 * full text as a tooltip on hover; one that fits shows none.
 */
test('elided labels show their full text on hover', async ({
  page,
  request,
}) => {
  // Long enough to be cut off in the sidebar and in the header alike.
  const dir = await tempProjectDir(
    `hs-ui-elided-${'a-project-name-long-enough-'.repeat(6)}`,
  );
  const dirName = basename(dir);
  const projectId = await addEmptyProject(request, dir);
  const branch = `feature/${'a-branch-name-too-long-to-fit-beside-its-name-'.repeat(3)}`;
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'elided', config: { branch } },
  });
  expect(res.status()).toBe(201);

  await page.goto('/');
  const projectButton = page
    .getByRole('navigation', { name: 'Projects' })
    .getByRole('button', { name: new RegExp(dirName) });
  const projectName = projectButton.getByText(dirName, { exact: true });
  await projectName.hover();
  await expect(projectName).toHaveAttribute('title', dirName);
  await projectButton.click();

  const path = page.getByText(dir, { exact: true });
  await path.hover();
  await expect(path).toHaveAttribute('title', dir);

  const card = page.getByRole('region', { name: 'Workstream elided' });
  const branchName = card.getByText(branch, { exact: true });
  await branchName.hover();
  await expect(branchName).toHaveAttribute('title', branch);

  const provider = card.getByText('Fake Provider', { exact: true });
  await provider.hover();
  await expect(provider).not.toHaveAttribute('title');
});

// The name gives way first: the branch beside it and the provider stay readable.
test('a long workstream name leaves its branch and provider readable', async ({
  page,
  request,
}) => {
  const dir = await tempProjectDir('hs-ui-elided-pills-');
  const projectId = await addEmptyProject(request, dir);
  const name = `a workstream name ${'long enough to crowd its pills '.repeat(4)}`;
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name, config: { branch: 'main' } },
  });
  expect(res.status()).toBe(201);

  await page.goto('/');
  await page
    .getByRole('navigation', { name: 'Projects' })
    .getByRole('button', { name: new RegExp(basename(dir)) })
    .click();
  const card = page.getByRole('region', { name: `Workstream ${name.trim()}` });
  const heading = card.getByRole('heading', { name: name.trim() });
  await expect(heading).toBeVisible();

  const fits = (text: string) =>
    card
      .getByText(text, { exact: true })
      .evaluate((element) => element.scrollWidth <= element.clientWidth);
  expect(await fits('Fake Provider')).toBe(true);
  expect(await fits('main')).toBe(true);
  expect(
    await heading.evaluate(
      (element) => element.scrollWidth > element.clientWidth,
    ),
  ).toBe(true);
});
