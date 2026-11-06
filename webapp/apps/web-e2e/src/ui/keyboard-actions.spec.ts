import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { searchCommands } from './keyboard-helpers';
import {
  firstSessionPath,
  openWorkstreamCard,
  runSession,
} from './workstream-helpers';

/**
 * What a card or row offers, reached from the keyboard: its actions menu on
 * Shift+F10, and the palette's commands for the selected item.
 */

/** A card whose one session is running, so it offers Stop. */
async function runningSession(page: Page, request: APIRequestContext) {
  const { card, row, workstreamName, projectId, workstreamId } =
    await openWorkstreamCard(page, request, 'hs-ui-kbd-actions', 'kbd-actions');
  await expect(row.getByTestId('session-state')).toHaveText('Idle', {
    timeout: 20_000,
  });
  const sessionPath = await firstSessionPath(request, projectId, workstreamId);
  await runSession(request, sessionPath);
  await expect(row.getByTestId('session-state')).toHaveText('Running');
  return { card, row, workstreamName, sessionPath };
}

test('Shift+F10 on a row opens its actions, and Stop stops it', async ({
  page,
  request,
}) => {
  const { row } = await runningSession(page, request);
  await row.focus();

  await page.keyboard.press('Shift+F10');
  const menu = page.getByRole('menu', { name: /^Actions for session / });
  // By name: an item's key hint is on screen beside it but not part of it.
  const names = [
    'Open',
    'Stop',
    'Restart with settings…',
    'Rename',
    'Show details',
    'Delete',
  ];
  const items = menu.getByRole('menuitem');
  await expect(items).toHaveCount(names.length);
  for (const [index, name] of names.entries()) {
    await expect(items.nth(index)).toHaveAccessibleName(name);
  }
  await expect(menu.getByRole('separator')).toHaveCount(1);
  await expect(menu.getByRole('menuitem', { name: 'Open' })).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Enter');

  await expect(menu).toHaveCount(0);
  await expect(row.getByTestId('session-state')).toHaveText('Stopped');
  await expect(row).toBeFocused();
});

// The hint is shown, not said: aria-keyshortcuts tells a screen reader.
test("a row's menu shows the keys of Rename and Delete beside them", async ({
  page,
  request,
}) => {
  const { row } = await runningSession(page, request);
  await row.focus();
  await page.keyboard.press('Shift+F10');
  const menu = page.getByRole('menu', { name: /^Actions for session / });
  for (const [name, keys, aria] of [
    ['Rename', 'F2', 'F2'],
    ['Delete', 'Delete', 'Delete'],
  ]) {
    const item = menu.getByRole('menuitem', { name, exact: true });
    const hint = item.locator('[aria-hidden="true"]');
    await expect(hint).toBeVisible();
    await expect(hint).toHaveText(keys);
    await expect(item).toHaveAttribute('aria-keyshortcuts', aria);
  }
  await page.keyboard.press('Escape');
  await expect(menu).toHaveCount(0);
});

test("a row's own keys stay out of its actions menu", async ({
  page,
  request,
}) => {
  const { row } = await runningSession(page, request);
  await row.focus();
  await page.keyboard.press('Shift+F10');
  const menu = page.getByRole('menu', { name: /^Actions for session / });
  await expect(menu).toBeVisible();

  await page.keyboard.press('F2');
  await expect(menu).toBeVisible();
  await expect(row.getByRole('textbox', { name: 'Session name' })).toHaveCount(
    0,
  );
  await page.keyboard.press('Escape');
  await expect(menu).toHaveCount(0);
  await expect(row).toBeFocused();
});

test('the palette renames the selected workstream', async ({
  page,
  request,
}) => {
  const { card, workstreamName } = await runningSession(page, request);
  await card.focus();

  const palette = await searchCommands(page, 'rename workstream');
  await expect(palette.getByRole('option').first()).toHaveText(
    `Workstream "${workstreamName}": Rename`,
  );
  await page.keyboard.press('Enter');

  await expect(
    card.getByRole('textbox', { name: 'Workstream name' }),
  ).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(card).toBeFocused();
});

// A click draws no focus ring, and the selection outlives focus leaving the
// list, so it is drawn on its own: what the palette acts on is always shown.
test('a clicked card shows as selected, and stays so for the palette', async ({
  page,
  request,
}) => {
  const { card, workstreamName } = await runningSession(page, request);
  const border = () =>
    card.evaluate(
      (element) =>
        (
          globalThis as unknown as {
            getComputedStyle: (e: unknown) => { borderTopColor: string };
          }
        ).getComputedStyle(element).borderTopColor,
    );
  const unselected = await border();

  await card.getByRole('heading', { name: workstreamName }).click();
  await expect(card).toHaveAttribute('aria-current', 'true');
  await expect.poll(border).not.toBe(unselected);

  await page.getByRole('button', { name: 'New workstream' }).focus();
  await expect(card).toHaveAttribute('aria-current', 'true');
  const palette = await searchCommands(page, 'rename workstream');
  await expect(palette.getByRole('option').first()).toHaveText(
    `Workstream "${workstreamName}": Rename`,
  );
});

test('a right-click on a row opens the same actions', async ({
  page,
  request,
}) => {
  const { row } = await runningSession(page, request);

  await row.getByTestId('session-title').click({ button: 'right' });
  const menu = page.getByRole('menu', { name: /^Actions for session / });
  await expect(menu.getByRole('menuitem', { name: 'Open' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(menu).toHaveCount(0);
  await expect(row).toBeFocused();
});

test("a right-click in a row's confirmation opens no actions menu", async ({
  page,
  request,
}) => {
  const { row } = await runningSession(page, request);
  await row.focus();
  await page.keyboard.press('Delete');
  const dialog = page.getByRole('dialog', { name: /^Delete / });
  await expect(dialog).toBeVisible();

  await dialog.getByText(/delete the session/).click({ button: 'right' });
  await expect(page.getByRole('menu')).toHaveCount(0);
  await expect(dialog).toBeVisible();
});

// A click outside closes the menu before any blur could say focus left the
// list, so the next state push must not take focus back to the card.
test('a click away from the menu leaves focus off the list', async ({
  page,
  request,
}) => {
  const { card, row, sessionPath } = await runningSession(page, request);
  await card.focus();
  await page.keyboard.press('Shift+F10');
  const menu = page.getByRole('menu', { name: /^Actions for workstream / });
  await expect(menu).toBeVisible();
  // Opening comes first, as in a session's; what deletes last, set apart. The
  // fake's folder is not on this host, so there is no plain Open, only modes.
  await expect(menu.getByRole('menuitem').first()).toHaveAccessibleName(
    /^Open with /,
  );
  await expect(menu.getByRole('menuitem').last()).toHaveAccessibleName(
    'Delete',
  );
  await expect(menu.getByRole('separator')).toHaveCount(1);

  const blank = await page.getByRole('main').boundingBox();
  await page.mouse.click(
    (blank?.x ?? 0) + (blank?.width ?? 0) / 2,
    (blank?.y ?? 0) + (blank?.height ?? 0) - 10,
  );
  await expect(menu).toHaveCount(0);
  expect((await request.post(`${sessionPath}/stop`)).status()).toBe(204);
  await expect(row.getByTestId('session-state')).toHaveText('Stopped');

  await expect(card).not.toBeFocused();
  await expect(row).not.toBeFocused();
});
