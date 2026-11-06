import {
  type APIRequestContext,
  type Locator,
  type Page,
} from '@playwright/test';
import { expect, test } from '../fixtures';
import { createWorkstream } from '../api/workstream-helpers';
import { loadApp } from './keyboard-helpers';
import {
  firstSessionPath,
  materializedWorkstream,
  openWorkstreamCard,
  runSession,
  selectProject,
  unique,
} from './workstream-helpers';

/**
 * The workstream cards and their session rows as one keyboard-driven list:
 * arrows move a visible focus between them, and the focused row answers Enter,
 * F2, Delete and Right/Left without a mouse.
 */

const liveCard = (page: Page, request: APIRequestContext, sessions = 1) =>
  openWorkstreamCard(page, request, 'hs-ui-kbd-list', 'kbd-list', sessions);

test('arrows move between the card and its session, which Enter opens', async ({
  page,
  request,
}) => {
  const { card, row } = await liveCard(page, request);

  await card.focus();
  await page.keyboard.press('ArrowDown');
  await expect(row).toBeFocused();
  await page.keyboard.press('ArrowLeft');
  await expect(card).toBeFocused();
  await page.keyboard.press('End');
  await expect(row).toBeFocused();
  await page.keyboard.press('Home');
  await expect(card).toBeFocused();
  await page.keyboard.press('ArrowDown');

  const open = page.waitForRequest(
    (req) => req.method() === 'POST' && req.url().endsWith('/open'),
  );
  await page.keyboard.press('Enter');
  expect((await open).postDataJSON()).toEqual({ mode: 'terminal' });
});

test('F2 renames the focused session and hands focus back', async ({
  page,
  request,
}) => {
  const { card, row } = await liveCard(page, request);

  await row.focus();
  await page.keyboard.press('F2');
  const box = card.getByRole('textbox', { name: 'Session name' });
  await expect(box).toBeFocused();
  await box.fill('Renamed by keyboard');
  await page.keyboard.press('Enter');

  await expect(card.getByTestId('session-title')).toHaveText(
    'Renamed by keyboard',
  );
  await expect(row).toBeFocused();
});

test('Right and Left open and close the session details', async ({
  page,
  request,
}) => {
  const { row } = await liveCard(page, request);
  const toggle = row.getByRole('button', { name: 'Toggle session details' });

  await row.focus();
  await page.keyboard.press('ArrowRight');
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await page.keyboard.press('ArrowLeft');
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
});

// Cmd+Backspace is a Mac's Delete; elsewhere Meta is the system's key, and
// the row says which keys it answers accordingly.
test('Meta+Backspace deletes nothing off a Mac', async ({ page, request }) => {
  const { row } = await liveCard(page, request);
  test.skip(
    await page.evaluate(() => /Mac/.test(globalThis.navigator.userAgent)),
    'Cmd+Backspace is the delete key on a Mac',
  );

  await expect(row).toHaveAttribute(
    'aria-keyshortcuts',
    'ArrowRight ArrowLeft Enter F2 Delete Shift+F10',
  );
  await row.focus();
  await page.keyboard.press('Meta+Backspace');
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

// Told once, on entering the list, rather than on every arrow press.
test('the list describes its keys, and its items do not', async ({
  page,
  request,
}) => {
  const { card, row } = await liveCard(page, request);

  const list = page.getByRole('group', { name: 'Workstreams' });
  await expect(list).toHaveAccessibleDescription(
    /Delete: Delete the focused workstream or session\./,
  );
  await expect(card).toHaveAccessibleDescription('');
  await expect(row).toHaveAccessibleDescription('');
});

test('Delete asks on Cancel; Escape and Enter both keep the row', async ({
  page,
  request,
}) => {
  const { card, row } = await liveCard(page, request);
  const dialog = page.getByRole('dialog', { name: 'Delete "Claude Code"?' });

  await row.focus();
  await page.keyboard.press('Delete');
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(row).toBeFocused();

  await page.keyboard.press('Delete');
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(dialog).toHaveCount(0);
  await expect(card.getByRole('listitem')).toHaveCount(1);
  await expect(row).toBeFocused();
});

test('Stop keeps focus as it turns into Start', async ({ page, request }) => {
  const { row, projectId, workstreamId } = await liveCard(page, request);
  await runSession(
    request,
    await firstSessionPath(request, projectId, workstreamId),
  );
  await expect(row.getByTestId('session-state')).toHaveText('Running');

  await row.getByRole('button', { name: 'Stop' }).focus();
  await page.keyboard.press('Enter');

  await expect(row.getByTestId('session-state')).toHaveText('Stopped');
  await expect(row.getByRole('button', { name: 'Start' })).toBeFocused();
});

test('Tab comes back to the row that last had focus', async ({
  page,
  request,
}) => {
  const { card, row } = await liveCard(page, request);

  await card.focus();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Shift+Tab');
  await expect(row).not.toBeFocused();
  await page.keyboard.press('Tab');
  await expect(row).toBeFocused();
});

test('removing the focused session leaves focus on its neighbour', async ({
  page,
  request,
}) => {
  const { card, row } = await liveCard(page, request, 2);

  await row.focus();
  await page.keyboard.press('Delete');
  await page
    .getByRole('dialog', { name: 'Delete "Claude Code"?' })
    .getByRole('button', { name: 'Delete' })
    .click();

  await expect(card.getByRole('listitem')).toHaveCount(1);
  await expect(card.getByRole('listitem')).toBeFocused();
});

test('Delete on an in-place card opens on Cancel; deleting moves on', async ({
  page,
  request,
}) => {
  const { dirName, projectId, workstreamName } = await materializedWorkstream(
    request,
    'hs-ui-kbd-delete-',
    'kbd-delete',
  );
  const neighbourName = unique('kbd-neighbour');
  await createWorkstream(request, projectId, neighbourName);
  await loadApp(page);
  await selectProject(page, dirName);
  const card = page.getByRole('region', {
    name: `Workstream ${workstreamName}`,
  });

  await card.focus();
  await page.keyboard.press('Delete');
  const dialog = page.getByRole('dialog', {
    name: `Delete "${workstreamName}"?`,
  });
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(dialog.getByRole('button', { name: 'Delete' })).toBeFocused();
  await page.keyboard.press('Enter');

  await expect(card).toHaveCount(0);
  await expect(
    page.getByRole('region', { name: `Workstream ${neighbourName}` }),
  ).toBeFocused();
});

test('deleting the last workstream leaves focus on New workstream', async ({
  page,
  request,
}) => {
  const { dirName, workstreamName } = await materializedWorkstream(
    request,
    'hs-ui-kbd-delete-last-',
    'kbd-delete-last',
  );
  await loadApp(page);
  await selectProject(page, dirName);
  const card = page.getByRole('region', {
    name: `Workstream ${workstreamName}`,
  });

  await card.focus();
  await page.keyboard.press('Delete');
  const dialog = page.getByRole('dialog', {
    name: `Delete "${workstreamName}"?`,
  });
  await dialog.getByRole('button', { name: 'Delete' }).focus();
  await page.keyboard.press('Enter');

  await expect(card).toHaveCount(0);
  await expect(
    page.getByRole('button', { name: 'New workstream' }).last(),
  ).toBeFocused();
});

// Enter must delete nothing.
test('Delete on a worktree card asks on Cancel, and Escape backs out', async ({
  page,
  request,
}) => {
  const { card, workstreamName } = await liveCard(page, request);

  await card.focus();
  await page.keyboard.press('Delete');
  const dialog = page.getByRole('dialog', {
    name: `Delete "${workstreamName}"?`,
  });
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await expect(
    dialog.getByRole('button', { name: 'Keep files' }),
  ).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(card).toBeFocused();
});

// A click focuses the card as well, but shows no ring: its keys stay inert
// until the keyboard moves focus.
test('Delete does nothing on a card focused by a click', async ({
  page,
  request,
}) => {
  const { card, row } = await liveCard(page, request);

  await card.getByRole('heading').click();
  await expect(card).toBeFocused();
  await page.keyboard.press('Delete');
  await page.keyboard.press('ArrowDown');
  await expect(row).toBeFocused();
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('Tab from a card walks its own controls and then leaves the list', async ({
  page,
  request,
}) => {
  const { card, row } = await liveCard(page, request);
  // The logs pane comes after the list, so it is where Tab should go next.
  await page.getByRole('button', { name: 'Logs' }).click();
  const logs = page.getByRole('region', { name: 'Logs' });
  await expect(logs).toBeVisible();
  // Playwright has no role-based check for focus inside an element.
  const focusIn = (region: Locator) => region.locator(':focus');

  await card.focus();
  let steps = 0;
  for (; steps < 20; steps++) {
    await page.keyboard.press('Tab');
    if ((await focusIn(card).count()) === 0) break;
    // The card's own controls, and none of its row's.
    await expect(row).not.toBeFocused();
    await expect(focusIn(row)).toHaveCount(0);
  }

  expect(steps).toBeGreaterThan(0);
  await expect(focusIn(logs)).toHaveCount(1);
});

test("a row's details button wears the app's focus ring", async ({
  page,
  request,
}) => {
  const { card, row } = await liveCard(page, request);

  await card.focus();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Tab');
  // Named by what it holds, the session's title first of all.
  const details = row.getByRole('button', { name: /Claude Code/ }).first();
  await expect(details).toBeFocused();
  const outline = await details.evaluate((element) => {
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
