import { basename } from 'node:path';
import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { git, gitProject } from '../git-repo';
import { loadApp } from './keyboard-helpers';
import {
  createWorkstreamInForm,
  openNewWorkstreamForm,
  selectProject,
  unique,
} from './workstream-helpers';

/**
 * The new-workstream form on a git-backed project, which is checked out on a
 * feature branch: the base ref starts at the repository's default branch, the
 * refs are suggested as you fill it in, and a branch left empty is named
 * after the workstream by the daemon.
 */

/** Opens a fresh git project's new-workstream form on the core provider's worktree strategy. */
async function openForm(page: Page, request: APIRequestContext) {
  const { dir } = await gitProject(request, 'hs-ui-refs-');
  await loadApp(page);
  await selectProject(page, basename(dir));
  const form = await openNewWorkstreamForm(page);
  // The demo daemon also registers the fake, and the pick-list remembers a
  // last-used choice.
  await form.getByRole('radio', { name: 'Default' }).click();
  await form.getByRole('radio', { name: 'Worktree' }).click();
  return { dir, form };
}

test('a click on the base ref lists the refs, and a click picks one', async ({
  page,
  request,
}) => {
  const { form } = await openForm(page, request);
  const baseRef = form.getByRole('combobox', { name: 'Base ref' });
  const suggestions = form.getByRole('listbox');

  await baseRef.click();
  await expect(suggestions).toBeVisible();
  await suggestions.getByRole('option', { name: 'v1.0' }).click();
  await expect(baseRef).toHaveValue('v1.0');
  await expect(suggestions).toBeHidden();
  await expect(baseRef).toBeFocused();
});

test('the base ref starts at the default branch and suggests the refs', async ({
  page,
  request,
}) => {
  const { dir, form } = await openForm(page, request);
  const baseRef = form.getByRole('combobox', { name: 'Base ref' });
  const suggestions = form.getByRole('listbox');
  await expect(baseRef).toHaveValue('main');

  await baseRef.focus();
  await page.keyboard.press('ArrowDown');
  await expect(suggestions).toBeVisible();
  await expect(suggestions.getByRole('option', { name: 'v1.0' })).toBeVisible();
  const selected = suggestions.getByRole('option', { selected: true });
  await expect(selected).toHaveText('main');
  for (let i = 0; i < 5 && (await selected.textContent()) !== 'feature'; i++) {
    await page.keyboard.press('ArrowDown');
  }
  await page.keyboard.press('Enter');
  await expect(baseRef).toHaveValue('feature');
  await expect(suggestions).toBeHidden();

  // Escape closes the list, not the form.
  await page.keyboard.press('ArrowDown');
  await expect(suggestions).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(suggestions).toBeHidden();
  await expect(form).toBeVisible();
  await expect(baseRef).toBeFocused();

  const name = unique('from-feature');
  await form.getByLabel('Name').fill(name);
  await form.getByRole('combobox', { name: 'Branch' }).fill(name);
  await createWorkstreamInForm(page, () =>
    form.getByRole('button', { name: /^Create$/ }).click(),
  );
  await expect(
    page.getByRole('region', { name: `Workstream ${name}` }),
  ).toBeVisible({ timeout: 20_000 });
  expect(await git(dir, 'rev-parse', name)).toBe(
    await git(dir, 'rev-parse', 'feature'),
  );
});

test('a branch left empty is named after the workstream', async ({
  page,
  request,
}) => {
  const { dir, form } = await openForm(page, request);
  const suffix = unique('');
  await form.getByLabel('Name').fill(`Fix login bug${suffix}`);
  // Left untouched, it shows the branch the daemon derives from the name.
  await expect(form.getByRole('combobox', { name: 'Branch' })).toHaveValue(
    `fix-login-bug${suffix}`,
  );

  await createWorkstreamInForm(page, () =>
    form.getByRole('button', { name: /^Create$/ }).click(),
  );
  const card = page.getByRole('region', {
    name: `Workstream Fix login bug${suffix}`,
  });
  await expect(
    card.getByText(`fix-login-bug${suffix}`, { exact: true }),
  ).toBeVisible({
    timeout: 20_000,
  });
  expect(await git(dir, 'rev-parse', `fix-login-bug${suffix}`)).toBe(
    await git(dir, 'rev-parse', 'main'),
  );
});

test('a name typed from the keyboard creates a worktree on its branch', async ({
  page,
  request,
}) => {
  const { dir, form } = await openForm(page, request);
  const name = form.getByLabel('Name');
  await name.focus();
  const workstreamName = unique('Nightly Build');
  await page.keyboard.type(workstreamName);
  await createWorkstreamInForm(page, () => page.keyboard.press('Enter'));
  const branch = workstreamName.toLowerCase().replace(' ', '-');
  await expect(
    page
      .getByRole('region', { name: `Workstream ${workstreamName}` })
      .getByText(branch, { exact: true }),
  ).toBeVisible({ timeout: 20_000 });
  expect(
    await git(dir, 'rev-parse', '--verify', `refs/heads/${branch}`),
  ).not.toBe('');
});
