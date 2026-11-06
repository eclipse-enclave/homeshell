import { expect, test } from '../fixtures';
import { selectEmptyProject } from './workstream-helpers';

/** The selected project's header — rename and delete — by keyboard. */

test('Alt+R renames the project, and Enter and Escape give focus back', async ({
  page,
  request,
}) => {
  const name = await selectEmptyProject(page, request, 'hs-ui-header-');
  // The header's, where focus goes back to; an empty project has a second.
  const start = page
    .getByRole('main')
    .locator('header')
    .first()
    .getByRole('button', { name: 'New workstream' });
  const box = page.getByRole('textbox', { name: 'Project name' });

  // No workstream is selected, so the page's letters are in reach.
  await start.focus();
  await page.keyboard.press('Alt+r');
  await expect(box).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(start).toBeFocused();

  await page.keyboard.press('Alt+r');
  await page.keyboard.type(`${name}-renamed`);
  await page.keyboard.press('Enter');
  await expect(
    page.getByRole('heading', { level: 1, name: `${name}-renamed` }),
  ).toBeVisible();
  await expect(start).toBeFocused();
});

test('double-clicking the project name renames it', async ({
  page,
  request,
}) => {
  const name = await selectEmptyProject(page, request, 'hs-ui-header-');
  await page.getByRole('heading', { level: 1, name }).dblclick();
  await expect(
    page.getByRole('textbox', { name: 'Project name' }),
  ).toBeFocused();
});

test('the header offers New workstream, then Delete, and no Rename button', async ({
  page,
  request,
}) => {
  const name = await selectEmptyProject(page, request, 'hs-ui-header-');
  const header = page.getByRole('main').locator('header').first();
  const labels = await header
    .getByRole('button')
    .evaluateAll((nodes) =>
      nodes.map(
        (node) =>
          node.getAttribute('aria-label') ?? node.textContent?.trim() ?? '',
      ),
    );
  expect(labels.filter((label) => !label.startsWith('Copy'))).toEqual([
    'New workstream',
    `Delete project ${name}`,
  ]);
});

test('Delete asks on Cancel, then focuses the project selected next', async ({
  page,
  request,
}) => {
  const name = await selectEmptyProject(page, request, 'hs-ui-header-');
  await page
    .getByRole('button', { name: `Delete project ${name}`, exact: true })
    .focus();
  await page.keyboard.press('Enter');

  const dialog = page.getByRole('dialog', { name: `Delete "${name}"?` });
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(dialog.getByRole('button', { name: 'Delete' })).toBeFocused();
  await page.keyboard.press('Enter');

  const nav = page.getByRole('navigation', { name: 'Projects' });
  await expect(nav.getByRole('button', { name: new RegExp(name) })).toHaveCount(
    0,
  );
  const selected = nav.locator('[aria-current="true"]');
  await expect(selected).toBeFocused();
});
