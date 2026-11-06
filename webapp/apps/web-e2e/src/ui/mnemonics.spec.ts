import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { loadApp } from './keyboard-helpers';
import {
  materializedWorkstream,
  openWorkstreamCard,
  selectProject,
} from './workstream-helpers';

/**
 * Alt's letters: holding Alt underlines the letter of each button it reaches,
 * and Alt+letter presses it. What is in reach is the selected card or row
 * first, then the page — the same letter means the same action on every item.
 */

const liveCard = (page: Page, request: APIRequestContext) =>
  openWorkstreamCard(page, request, 'hs-ui-mnemonic', 'mnemonic');

const SHOWN = 'data-mnemonic-shown';

test('holding Alt marks the letters the selected card and the page reach', async ({
  page,
  request,
}) => {
  const { card, row, workstreamName } = await liveCard(page, request);
  await card.getByRole('heading', { name: workstreamName }).click();
  await expect(card).toHaveAttribute('aria-current', 'true');

  const newSession = card.getByRole('button', { name: 'New session' });
  const newWorkstream = page.getByRole('button', { name: 'New workstream' });
  const deleteProject = page.getByRole('button', { name: /^Delete project / });
  const openSession = row.getByRole('button', { name: 'Open', exact: true });

  await page.keyboard.down('Alt');
  await expect(newSession).toHaveAttribute(SHOWN, '');
  // The page's own, where the card leaves the letter free…
  await expect(newWorkstream).toHaveAttribute(SHOWN, '');
  // …and the project's Delete never: D is an item's.
  await expect(deleteProject).not.toHaveAttribute(SHOWN);
  // A row inside the card is not the selection, so its letters are not in reach.
  await expect(openSession).not.toHaveAttribute(SHOWN);
  // The marked letter is underlined.
  const underline = await newSession.getByText('N', { exact: true }).evaluate(
    (element) =>
      (
        globalThis as unknown as {
          getComputedStyle: (e: unknown) => { textDecorationLine: string };
        }
      ).getComputedStyle(element).textDecorationLine,
  );
  expect(underline).toBe('underline');

  await page.keyboard.up('Alt');
  await expect(newSession).not.toHaveAttribute(SHOWN);
});

test('Alt+N starts a session in the selected workstream', async ({
  page,
  request,
}) => {
  const { card, workstreamName } = await liveCard(page, request);
  await card.getByRole('heading', { name: workstreamName }).click();

  await page.keyboard.press('Alt+n');
  await expect(card.getByRole('form', { name: 'New session' })).toBeVisible();
});

// Once the last item is gone nothing is selected; D must not fall through to
// deleting the project.
test('Alt+D with nothing selected deletes nothing', async ({
  page,
  request,
}) => {
  const { card } = await liveCard(page, request);
  await expect(card).not.toHaveAttribute('aria-current', 'true');
  await page.keyboard.press('Alt+d');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(card).toBeVisible();
});

// Focus on a control outside the list is work on something else: the
// selection's letters stay with the selection.
test("a control outside the list keeps the selection's letters from it", async ({
  page,
  request,
}) => {
  const { card, workstreamName } = await liveCard(page, request);
  await card.getByRole('heading', { name: workstreamName }).click();
  await expect(card).toHaveAttribute('aria-current', 'true');
  await page.getByRole('button', { name: 'New workstream' }).focus();
  await expect(card).toHaveAttribute('aria-current', 'true');

  await page.keyboard.press('Alt+d');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await page.keyboard.press('Alt+n');
  await expect(card.getByRole('form', { name: 'New session' })).toHaveCount(0);
  await expect(card).toBeVisible();
});

// A form inside the card is work of its own: Alt+D there must not delete it.
test("a form inside the selected card keeps the card's letters from it", async ({
  page,
  request,
}) => {
  const { card, workstreamName } = await liveCard(page, request);
  await card.getByRole('heading', { name: workstreamName }).click();
  await page.keyboard.press('Alt+n');
  await card
    .getByRole('form', { name: 'New session' })
    .getByRole('button', { name: 'Cancel' })
    .focus();
  await expect(card).toHaveAttribute('aria-current', 'true');

  await page.keyboard.press('Alt+d');
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

// The send box has no letters of its own, so it takes none from the page, but
// it is in the row: the row's and card's letters stay out of reach.
test("a session's send box leaves the page's letters in reach", async ({
  page,
  request,
}) => {
  const { card, row, workstreamName } = await liveCard(page, request);
  await expect(row.getByTestId('session-state')).toHaveText('Idle', {
    timeout: 20_000,
  });
  await row.focus();
  await page.keyboard.press('ArrowRight');
  await card.getByRole('button', { name: 'Send', exact: true }).focus();

  await page.keyboard.down('Alt');
  await expect(
    page.getByRole('button', { name: 'New workstream' }),
  ).toHaveAttribute(SHOWN, '');
  await expect(
    row.getByRole('button', { name: /^Delete session/ }),
  ).not.toHaveAttribute(SHOWN);
  await expect(
    card.getByRole('button', { name: `Delete workstream ${workstreamName}` }),
  ).not.toHaveAttribute(SHOWN);
  await page.keyboard.up('Alt');

  await page.keyboard.press('Alt+d');
  await page.keyboard.press('Alt+w');
  await expect(
    page.getByRole('form', { name: 'New workstream' }),
  ).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('an open menu keeps the page letters from it', async ({
  page,
  request,
}) => {
  const { card } = await liveCard(page, request);
  await card.focus();
  await page.keyboard.press('Shift+F10');
  await expect(page.getByRole('menu')).toBeVisible();

  await page.keyboard.press('Alt+w');
  await expect(page.getByRole('form', { name: 'New workstream' })).toHaveCount(
    0,
  );
});

// A real folder gives Open a default half: Alt+O presses it, Alt+Shift+O the
// caret beside it.
test('Alt+Shift+O opens the Open menu of the selected card', async ({
  page,
  request,
}) => {
  const { dirName, workstreamName } = await materializedWorkstream(
    request,
    'hs-ui-mnemonic-split-',
    'split',
  );
  await loadApp(page);
  await selectProject(page, dirName);
  const card = page.getByRole('region', {
    name: `Workstream ${workstreamName}`,
  });
  await card.focus();
  await expect(card).toHaveAttribute('aria-current', 'true');

  await page.keyboard.press('Alt+Shift+O');
  // Nothing in it is picked: the folder is real, and a terminal would open.
  await expect(
    page.getByRole('menu', { name: `Open workstream ${workstreamName}` }),
  ).toBeVisible();
  await page.keyboard.press('Escape');
});

test('Alt+D on a selected session asks first, and Alt+C cancels', async ({
  page,
  request,
}) => {
  const { card, row } = await liveCard(page, request);
  await row.focus();
  await expect(row).toHaveAttribute('aria-current', 'true');

  await page.keyboard.press('Alt+d');
  const dialog = page.getByRole('dialog', { name: 'Delete "Claude Code"?' });
  await expect(dialog).toBeVisible();
  // The dialog is the only scope while it is open.
  await page.keyboard.down('Alt');
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toHaveAttribute(
    SHOWN,
    '',
  );
  await expect(
    card.getByRole('button', { name: 'New session' }),
  ).not.toHaveAttribute(SHOWN);
  await page.keyboard.up('Alt');

  await page.keyboard.press('Alt+c');
  await expect(dialog).toHaveCount(0);
  await expect(card.getByRole('listitem')).toHaveCount(1);
});

test('the tooltips name the keys', async ({ page, request }) => {
  const { card, row, workstreamName } = await liveCard(page, request);
  await expect(
    card.getByRole('button', { name: `Delete workstream ${workstreamName}` }),
  ).toHaveAttribute('title', /\(Delete, (Alt\+|⌥)D\)$/);
  await expect(
    card.getByRole('button', { name: 'New session' }),
  ).toHaveAttribute('title', /\((Alt\+|⌥)N\)$/);
  await expect(row.getByTestId('session-title')).toHaveAttribute(
    'title',
    'Claude Code\n\nDouble-click to rename (or press F2)',
  );
});

// R is on the title, not on a button: Alt+R renames the selection, and
// Escape gives focus back to it.
test('Alt+R renames the selected card or session, and Escape gives focus back', async ({
  page,
  request,
}) => {
  const { card, row, workstreamName } = await liveCard(page, request);
  const cardTitle = card.getByRole('heading', { name: workstreamName });
  const sessionTitle = row.getByTestId('session-title');

  await card.focus();
  await expect(card).toHaveAttribute('aria-current', 'true');
  await page.keyboard.down('Alt');
  await expect(cardTitle).toHaveAttribute(SHOWN, '');
  await expect(sessionTitle).not.toHaveAttribute(SHOWN);
  await page.keyboard.press('r');
  await page.keyboard.up('Alt');
  const cardBox = card.getByRole('textbox', { name: 'Workstream name' });
  await expect(cardBox).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(card).toBeFocused();

  await row.focus();
  await expect(row).toHaveAttribute('aria-current', 'true');
  await page.keyboard.down('Alt');
  await expect(sessionTitle).toHaveAttribute(SHOWN, '');
  await expect(cardTitle).not.toHaveAttribute(SHOWN);
  await page.keyboard.press('r');
  await page.keyboard.up('Alt');
  const rowBox = row.getByRole('textbox', { name: 'Session name' });
  await expect(rowBox).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(row).toBeFocused();
});

test('Alt does nothing in a text field outside a dialog', async ({
  page,
  request,
}) => {
  const { card, workstreamName } = await liveCard(page, request);
  await card.getByRole('heading', { name: workstreamName }).click();
  await card.getByRole('heading', { name: workstreamName }).dblclick();
  const box = card.getByRole('textbox', { name: 'Workstream name' });
  await expect(box).toBeFocused();

  await page.keyboard.press('Alt+n');
  await expect(card.getByRole('form', { name: 'New session' })).toHaveCount(0);
  await expect(box).toBeFocused();
});

// Browse is only looked at: pressing it would open the desktop's folder chooser.
test('the Add project form marks its letters from its path box, and Alt+C cancels', async ({
  page,
}) => {
  await loadApp(page);
  await page.getByRole('button', { name: 'Add project' }).click();
  const form = page.getByRole('form', { name: 'Add project' });
  await form.getByPlaceholder('/path/to/project').focus();

  await page.keyboard.down('Alt');
  for (const name of ['Add', 'Browse', 'Cancel']) {
    await expect(
      form.getByRole('button', { name, exact: true }),
    ).toHaveAttribute(SHOWN, '');
  }
  await page.keyboard.up('Alt');

  await page.keyboard.press('Alt+c');
  await expect(form).toHaveCount(0);
});

test('the New session form marks its letters, and Alt+C cancels', async ({
  page,
  request,
}) => {
  const { card, workstreamName } = await liveCard(page, request);
  await card.getByRole('heading', { name: workstreamName }).click();
  await page.keyboard.press('Alt+n');
  const form = card.getByRole('form', { name: 'New session' });
  await expect(form).toBeVisible();

  await page.keyboard.down('Alt');
  for (const name of ['Start session', 'Cancel']) {
    await expect(
      form.getByRole('button', { name, exact: true }),
    ).toHaveAttribute(SHOWN, '');
  }
  await page.keyboard.up('Alt');

  await page.keyboard.press('Alt+c');
  await expect(form).toHaveCount(0);
});
