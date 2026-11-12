import { basename } from 'node:path';
import {
  type APIRequestContext,
  type Locator,
  type Page,
} from '@playwright/test';
import { expect, test } from '../fixtures';
import { gitProject } from '../git-repo';
import { withSettingsLock } from '../settings-lock';
import { textContrast, useTheme } from './contrast';
import { loadApp } from './keyboard-helpers';
import {
  createWorkstreamInForm,
  openNewWorkstreamForm,
  selectProject,
  unique,
} from './workstream-helpers';

/**
 * What the create form does with input the daemon will not accept.
 *
 * The behaviour that matters to a user: the refusal names the field they got
 * wrong, the form stays open with what they typed still in it, nothing is created
 * in the background, and correcting the value and resubmitting works. Before the
 * daemon validated this synchronously, a branch like `my branch` produced a card
 * that turned red a moment later — the form had already closed and declared
 * success.
 */

/** Opens a fresh git project's new-workstream form, on the core provider's worktree strategy. */
async function openForm(page: Page, request: APIRequestContext) {
  const { dir } = await gitProject(request, 'hs-ui-formvalid-');
  await loadApp(page);
  await selectProject(page, basename(dir));
  const form = await openNewWorkstreamForm(page);
  // The core provider is the one with the branch rules; the demo daemon also
  // registers the fake, and the pick-list remembers a last-used choice.
  await form.getByRole('radio', { name: 'Default' }).click();
  await form.getByRole('radio', { name: 'worktree' }).click();
  return form.getByLabel(/^Branch/i);
}

/** Expects `field` marked refused, with a reason under it that matches `reason`. */
async function expectRefused(field: Locator, reason: RegExp | string) {
  await expect(field).toHaveAttribute('aria-invalid', 'true', {
    timeout: 15_000,
  });
  await expect(field).toHaveAccessibleDescription(reason);
}

test('a branch git would reject is refused, and the form keeps what you typed', async ({
  page,
  request,
}) => {
  const branch = await openForm(page, request);

  await branch.fill('my branch');
  await page.getByRole('button', { name: /^Create$/ }).click();

  // The refusal is said under the field it is about, and quotes the value.
  await expectRefused(branch, /my branch/);
  await expect(
    page.getByRole('form', { name: 'New workstream' }).getByRole('alert'),
  ).toHaveCount(0);

  // The form is still open on the offending value — nothing to retype — and
  // focus is on it.
  await expect(branch).toHaveValue('my branch');
  await expect(branch).toBeFocused();
  // And nothing was created, not even a card that fails a moment later.
  await expect(page.getByRole('region', { name: /^Workstream / })).toHaveCount(
    0,
  );
});

test('correcting the branch and resubmitting works', async ({
  page,
  request,
}) => {
  const branch = await openForm(page, request);

  await branch.fill('my branch');
  await page.getByRole('button', { name: /^Create$/ }).click();
  await expectRefused(branch, /my branch/);

  await branch.fill('fix/ok');
  // Named here rather than left to the default: the name a blank field falls back
  // to is `workstreams.nameTemplate`, which is one daemon-wide setting the specs
  // share — another spec configuring a template would otherwise decide what this
  // card is called.
  await page.getByLabel(/^Name/i).fill('fix/ok');
  await createWorkstreamInForm(page, () =>
    page.getByRole('button', { name: /^Create$/ }).click(),
  );

  await expect(
    page.getByRole('region', { name: 'Workstream fix/ok' }),
  ).toBeVisible({ timeout: 20_000 });
});

// An empty branch is the daemon's to name, after the workstream; only a name
// it can make no branch of is refused, and the refusal is about the branch.
test('a name no branch can be made of is refused on the branch', async ({
  page,
  request,
}) => {
  const branch = await openForm(page, request);

  await page.getByLabel(/^Name/i).fill('???');
  await expect(branch).toHaveValue('');
  await page.getByRole('button', { name: /^Create$/ }).click();

  await expectRefused(branch, /\?\?\?/);
  await expect(branch).toBeFocused();
  await expect(page.getByRole('region', { name: /^Workstream / })).toHaveCount(
    0,
  );
});

test('the reason under a refused field reads in the light theme', async ({
  page,
  request,
}) => {
  await useTheme(page, 'light');
  const branch = await openForm(page, request);

  await branch.fill('my branch');
  await page.getByRole('button', { name: /^Create$/ }).click();
  await expectRefused(branch, /my branch/);

  const reason = page
    .getByRole('form', { name: 'New workstream' })
    .getByText(/my branch/);
  expect(await textContrast(reason)).toBeGreaterThanOrEqual(4.5);
});

// The folder is taken by the project's in-place workstream: the refusal is said
// on the strategy, while the form is still open to pick another one.
test('a second in-place workstream is refused on the strategy, with the form open', async ({
  page,
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-ui-inplace-');
  const first = unique('first');
  expect(
    (
      await request.post(`/api/projects/${projectId}/workstreams`, {
        data: {
          providerId: 'core',
          name: first,
          config: { strategy: 'in-place' },
        },
      })
    ).status(),
  ).toBe(201);
  await loadApp(page);
  await selectProject(page, basename(dir));
  await expect(
    page.getByRole('region', { name: `Workstream ${first}` }),
  ).toBeVisible({ timeout: 20_000 });

  const form = await openNewWorkstreamForm(page);
  await form.getByRole('radio', { name: 'Default' }).click();
  await form.getByRole('radio', { name: 'in-place' }).click();
  await form.getByLabel(/^Name/i).fill('second');
  await form.getByRole('button', { name: /^Create$/ }).click();

  // Said before what the field does, which describes it as well.
  await expectRefused(
    form.getByRole('radiogroup', { name: 'Filesystem strategy' }),
    new RegExp(
      `^"${first}" already works in this folder; choose worktree or copy`,
    ),
  );
  // Only the refused choice is painted as refused, not the ones it points to:
  // they keep the border of a pill in a group nothing refused.
  // Read once its transitions end: clicking "Default" fades it out of the
  // checked look, and near the end of that fade its border reads as
  // rgba(…, 1), which equals no settled border.
  const unrefused = await form
    .getByRole('radio', { name: 'Fake Provider' })
    .evaluate(async (pill) => {
      const el = pill as unknown as {
        getAnimations: () => { finished: Promise<unknown> }[];
      };
      for (let a = el.getAnimations(); a.length; a = el.getAnimations()) {
        await Promise.allSettled(a.map((anim) => anim.finished));
      }
      return (
        globalThis as unknown as {
          getComputedStyle: (e: unknown) => { borderColor: string };
        }
      ).getComputedStyle(pill).borderColor;
    });
  for (const name of ['worktree', 'copy']) {
    await expect(form.getByRole('radio', { name })).toHaveCSS(
      'border-color',
      unrefused,
    );
  }
  await expect(form.getByRole('radio', { name: 'in-place' })).not.toHaveCSS(
    'border-color',
    unrefused,
  );
  await expect(form.getByLabel(/^Name/i)).toHaveValue('second');
  await expect(page.getByRole('region', { name: /^Workstream / })).toHaveCount(
    1,
  );
});

/**
 * Creates a copy workstream, under the settings lock: where a copy goes is the
 * workstreams.location setting, which other tests set to a folder inside the
 * project, where copies are refused. Returns the response status.
 */
const createCopy = (request: APIRequestContext, projectId: string, name: string) =>
  withSettingsLock(async () =>
    (
      await request.post(`/api/projects/${projectId}/workstreams`, {
        data: { providerId: 'core', name, config: { strategy: 'copy' } },
      })
    ).status(),
  );

test('a name already in the project is refused on the name, with the form open', async ({
  page,
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-ui-dupname-');
  const taken = unique('copy one');
  expect(await createCopy(request, projectId, taken)).toBe(201);
  await loadApp(page);
  await selectProject(page, basename(dir));
  await expect(
    page.getByRole('region', { name: `Workstream ${taken}` }),
  ).toBeVisible({ timeout: 20_000 });

  const form = await openNewWorkstreamForm(page);
  await form.getByRole('radio', { name: 'Default' }).click();
  const name = form.getByLabel(/^Name/i);
  await name.fill(taken);
  await form.getByRole('button', { name: /^Create$/ }).click();

  await expectRefused(
    name,
    `"${taken}" is already used in this project; enter another`,
  );
  await expect(name).toHaveValue(taken);
  await expect(name).toBeFocused();
  await expect(form.getByRole('alert')).toHaveCount(0);
  await expect(page.getByRole('region', { name: /^Workstream / })).toHaveCount(
    1,
  );
});

test('an inline rename to a taken name is refused in the words a create uses', async ({
  page,
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-ui-dupren-');
  const taken = unique('Copy one');
  const other = unique('other');
  for (const name of [taken, other]) {
    expect(await createCopy(request, projectId, name)).toBe(201);
  }
  await loadApp(page);
  await selectProject(page, basename(dir));
  const card = page.getByRole('region', { name: `Workstream ${other}` });
  await expect(card).toBeVisible({ timeout: 20_000 });

  await card.getByRole('heading', { name: other }).dblclick();
  const input = card.getByRole('textbox', { name: 'Workstream name' });
  await input.fill(taken.toLowerCase());
  await input.press('Enter');

  await expect(card.getByRole('alert')).toHaveText(
    `"${taken}" is already used in this project; enter another`,
  );
  await expect(input).toHaveValue(taken.toLowerCase());
});

// The reported bug: a refusal stayed on screen after the value it was about had
// been corrected, until the form was submitted again. Editing the field is
// what takes it away — the reason is about a value no longer there.
test('a relative destination path is refused, and the refusal goes once the path is edited', async ({
  page,
  request,
}) => {
  const branch = await openForm(page, request);
  await branch.fill(unique('topic/relative-path'));
  const path = page.getByLabel(/^Destination path/i);

  await path.fill('relative/dir');
  await page.getByRole('button', { name: /^Create$/ }).click();
  await expectRefused(path, /absolute/);

  await path.fill('/tmp/absolute-dir');
  await expect(path).not.toHaveAttribute('aria-invalid', 'true');
  // What the field does still describes it; the reason no longer does.
  await expect(path).not.toHaveAccessibleDescription(/absolute/);
  // Still nothing created: editing withdrew the message, not the form.
  await expect(page.getByRole('region', { name: /^Workstream / })).toHaveCount(
    0,
  );
});
