import { basename } from 'node:path';
import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { gitProject } from '../git-repo';
import { loadApp } from './keyboard-helpers';
import { openNewWorkstreamForm, selectProject } from './workstream-helpers';

/**
 * The new-workstream form shows what the daemon would derive for each field
 * left as it is — the name, a worktree's branch, its directory — and keeps it
 * following what the user types, until they type over it.
 */

test.use({ viewport: { width: 1400, height: 900 } });

/**
 * Shows this page no name template. The template is one daemon-wide setting
 * other specs leave configured, and the form prefills the name from it, which
 * would stand in for the name the daemon derives.
 */
async function withoutNameTemplate(page: Page) {
  await page.route('**/api/settings', async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const response = await route.fetch();
    const settings = await response.json();
    settings.workstreams.nameTemplate = '';
    await route.fulfill({ response, json: settings });
  });
}

/** A fresh git project's form, on the core provider, with focus in the name. */
async function openForm(page: Page, request: APIRequestContext) {
  const { dir } = await gitProject(request, 'hs-ui-wsform-');
  await withoutNameTemplate(page);
  await loadApp(page);
  await selectProject(page, basename(dir));
  const form = await openNewWorkstreamForm(page);
  // The pick-list remembers a last-used provider; the core is the one asked.
  await form.getByRole('radio', { name: 'Default' }).click();
  // Asking for the open form again moves focus back to its name, selected.
  await openNewWorkstreamForm(page);
  return { form, dir, name: form.getByLabel('Name', { exact: true }) };
}

test('the name is filled in and selected, so typing replaces it', async ({
  page,
  request,
}) => {
  const { form, dir, name } = await openForm(page, request);
  await expect(name).toHaveValue(basename(dir));
  await expect(name).toBeFocused();

  await page.keyboard.type('Fix Login');
  await expect(name).toHaveValue('Fix Login');

  await form.getByRole('radio', { name: 'Worktree' }).click();
  await expect(form.getByLabel(/^Branch/)).toHaveValue('fix-login');
  await expect(form.getByLabel(/^Destination path/)).toHaveValue(
    /\/hs-ui-wsform-[^/]*-fix-login$/,
  );
});

test('a name cleared shows the generated one again', async ({
  page,
  request,
}) => {
  const { dir, name } = await openForm(page, request);
  await page.keyboard.type('Fix Login');
  await expect(name).toHaveValue('Fix Login');

  await name.fill('');
  await expect(name).toHaveValue(basename(dir));
});

test('a branch typed over stays, and the path follows the name', async ({
  page,
  request,
}) => {
  const { form, name } = await openForm(page, request);
  await form.getByRole('radio', { name: 'Worktree' }).click();
  const branch = form.getByLabel(/^Branch/);
  await branch.fill('topic');

  await name.fill('Something else');
  await expect(form.getByLabel(/^Destination path/)).toHaveValue(
    /-something-else$/,
  );
  await expect(branch).toHaveValue('topic');
});

test('a branch cleared shows the derived one again, and typing replaces it', async ({
  page,
  request,
}) => {
  const { form } = await openForm(page, request);
  await page.keyboard.type('Fix Login');
  await form.getByRole('radio', { name: 'Worktree' }).click();
  const branch = form.getByLabel(/^Branch/);
  await branch.fill('topic');

  await branch.fill('');
  await expect(branch).toHaveValue('fix-login');
  await page.keyboard.type('mine');
  await expect(branch).toHaveValue('mine');
});

test('the form spans the view', async ({ page, request }) => {
  const { form } = await openForm(page, request);
  const bounds = await form.boundingBox();
  expect(bounds?.width ?? 0).toBeGreaterThan(700);
});

test("Alt marks the form's letters from its text field, and Alt+C cancels", async ({
  page,
  request,
}) => {
  const { form, name } = await openForm(page, request);
  await expect(name).toBeFocused();

  await page.keyboard.down('Alt');
  for (const button of ['Cancel', 'Create']) {
    await expect(
      form.getByRole('button', { name: button, exact: true }),
    ).toHaveAttribute('data-mnemonic-shown', '');
  }
  await expect(
    page.getByRole('button', { name: 'New workstream' }),
  ).not.toHaveAttribute('data-mnemonic-shown');
  await page.keyboard.press('c');
  await page.keyboard.up('Alt');
  await expect(form).toHaveCount(0);
});
