import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { loadApp } from './keyboard-helpers';
import {
  createWorkstreamInForm,
  openWorkstreamCard,
  selectEmptyProject,
  unique,
  waitForRealCard,
} from './workstream-helpers';

/**
 * The create forms driven from the keyboard alone: each opens on the field
 * there is to fill in, and Enter submits it.
 */

/** A fixed tree for the in-app folder picker: /e2e holds projects, which holds app. */
const TREE: Record<string, string[]> = {
  '/e2e': ['projects', 'notes'],
  '/e2e/projects': ['app'],
  '/e2e/projects/app': [],
};

/** Opens a fresh, empty project's new-workstream form by keyboard. */
async function openWorkstreamForm(page: Page, request: APIRequestContext) {
  await selectEmptyProject(page, request, 'hs-ui-kbd-form-');
  await page.getByRole('button', { name: 'New workstream' }).focus();
  await page.keyboard.press('Enter');
  const form = page.getByRole('form', { name: 'New workstream' });
  const name = form.getByLabel('Name');
  await expect(name).toBeFocused({ timeout: 20_000 });
  return { form, name };
}

test('Escape closes the new-workstream form, back on its opener', async ({
  page,
  request,
}) => {
  const { form } = await openWorkstreamForm(page, request);

  await page.keyboard.press('Escape');
  await expect(form).toHaveCount(0);
  await expect(
    page.getByRole('button', { name: 'New workstream' }),
  ).toBeFocused();
});

test('Escape while an input method composes keeps the form open', async ({
  page,
  request,
}) => {
  const { form, name } = await openWorkstreamForm(page, request);

  await name.dispatchEvent('keydown', {
    key: 'Escape',
    isComposing: true,
    bubbles: true,
  });
  await expect(form).toBeVisible();
  await expect(name).toBeFocused();
});

test('a workstream created by keyboard alone ends up focused', async ({
  page,
  request,
}) => {
  const { form, name } = await openWorkstreamForm(page, request);
  const workstreamName = unique('kbd-form');
  await page.keyboard.type(workstreamName);

  // Which provider is preselected is the last one used; this wants the fake.
  await page.keyboard.press('Shift+Tab');
  const fake = form.getByRole('radio', { name: 'Fake Provider' });
  for (
    let i = 0;
    i < 5 && (await fake.getAttribute('aria-checked')) !== 'true';
    i++
  ) {
    await page.keyboard.press('ArrowRight');
  }
  await expect(fake).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(name).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(form.getByRole('textbox', { name: 'Branch' })).toBeFocused();
  await page.keyboard.type('main');
  await createWorkstreamInForm(page, () => page.keyboard.press('Enter'));

  const card = page.getByRole('region', {
    name: `Workstream ${workstreamName}`,
  });
  await waitForRealCard(card);
  await expect(card).toBeFocused();
});

/**
 * Opens the add-project form and its in-app folder picker by keyboard, over a
 * fixed tree: there is no desktop chooser, so Browse falls back to the picker.
 */
async function openPicker(page: Page, by: 'keyboard' | 'mouse' = 'keyboard') {
  await page.route('**/api/browse/native', (route) =>
    route.fulfill({ status: 501, json: { error: 'no native folder chooser' } }),
  );
  await page.route(/\/api\/browse(\?|$)/, (route) => {
    const path =
      new URL(route.request().url()).searchParams.get('path') ?? '/e2e';
    return route.fulfill({
      json: {
        path,
        parent: path === '/e2e' ? '' : path.slice(0, path.lastIndexOf('/')),
        entries: TREE[path].map((name) => ({
          name,
          path: `${path}/${name}`,
          isDir: true,
        })),
      },
    });
  });
  await loadApp(page);

  const browse = page.getByRole('button', { name: 'Browse' });
  if (by === 'mouse') {
    await page.getByRole('button', { name: 'Add project' }).click();
    await browse.click();
  } else {
    await page.getByRole('button', { name: 'Add project' }).focus();
    await page.keyboard.press('Enter');
    await expect(page.getByPlaceholder('/path/to/project')).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(browse).toBeFocused();
    await page.keyboard.press('Enter');
  }

  const picker = page.getByRole('dialog', { name: 'Select folder' });
  const entry = (name: string) =>
    picker.getByRole('button', { name: new RegExp(`${name}$`) });
  await expect(entry('projects')).toBeFocused();
  return { browse, picker, entry };
}

test('the folder picker is walked by keyboard and gives focus back to Browse', async ({
  page,
}) => {
  const { browse, picker, entry } = await openPicker(page);

  await page.keyboard.press('Enter');
  await expect(entry('app')).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(picker.getByTestId('current-path')).toHaveText(
    '/e2e/projects/app',
  );
  await expect(picker.getByRole('list', { name: 'Folders' })).toBeFocused();

  await page.keyboard.press('Escape');
  await expect(picker).toHaveCount(0);
  await expect(browse).toBeFocused();
});

test('the folder picker names the keys that open, go up and select', async ({
  page,
}) => {
  const { picker } = await openPicker(page);

  await expect(picker.getByText(/^Enter open$/)).toBeVisible();
  await expect(picker.getByText(/^(Backspace|⌫) up$/)).toBeVisible();
  await expect(picker.getByText(/^(Ctrl\+Enter|⌘Enter) select$/)).toBeVisible();
});

test("the folder picker's focused entry wears the app's focus ring", async ({
  page,
}) => {
  const { picker, entry } = await openPicker(page);
  await expect(
    picker.getByRole('heading', { name: 'Select folder', exact: true }),
  ).toBeVisible();

  const outline = await entry('projects').evaluate((element) => {
    const style = (
      globalThis as unknown as {
        getComputedStyle: (e: unknown) => {
          outlineStyle: string;
          outlineWidth: string;
        };
      }
    ).getComputedStyle(element);
    return { style: style.outlineStyle, width: style.outlineWidth };
  });
  // The browser's own ring is style `auto`.
  expect(outline).toEqual({ style: 'solid', width: '2px' });
});

// Enter acts on it however the picker was opened, so it must look it.
test("the folder picker's focused entry stands out when opened by mouse", async ({
  page,
}) => {
  const { entry } = await openPicker(page, 'mouse');
  await page.mouse.move(0, 0);
  const background = (name: string) =>
    entry(name).evaluate(
      (element) =>
        (
          globalThis as unknown as {
            getComputedStyle: (e: unknown) => { backgroundColor: string };
          }
        ).getComputedStyle(element).backgroundColor,
    );
  expect(await background('projects')).not.toBe(await background('notes'));
});

test('Backspace goes up and Ctrl/Cmd+Enter selects the folder shown', async ({
  page,
}) => {
  const { picker, entry } = await openPicker(page);

  await page.keyboard.press('Enter');
  await expect(entry('app')).toBeFocused();
  await page.keyboard.press('Backspace');
  await expect(picker.getByTestId('current-path')).toHaveText('/e2e');
  await expect(entry('projects')).toBeFocused();

  await page.keyboard.press('Enter');
  await expect(entry('app')).toBeFocused();
  await page.keyboard.press('ControlOrMeta+Enter');
  await expect(picker).toHaveCount(0);
  await expect(page.getByPlaceholder('/path/to/project')).toHaveValue(
    '/e2e/projects',
  );
});

// The form opens on its first pill; Enter there starts the session, as it
// would on a native radio.
test('a session started by keyboard alone: its key, then Enter', async ({
  page,
  request,
}) => {
  const { card } = await openWorkstreamCard(
    page,
    request,
    'hs-ui-kbd-session-',
    'kbd',
    0,
  );
  await card.focus();
  await page.keyboard.press('Control+Shift+Backquote');
  const form = card.getByRole('form', { name: 'New session' });
  await expect(form.getByRole('radio').first()).toBeFocused({
    timeout: 20_000,
  });

  await page.keyboard.press('Enter');
  await expect(form).toHaveCount(0);
  await expect(card.getByTestId('session-title')).toHaveText(['Claude Code'], {
    timeout: 15_000,
  });
});
