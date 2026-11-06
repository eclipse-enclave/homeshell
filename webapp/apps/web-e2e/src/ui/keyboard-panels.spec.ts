import { expect, test } from '../fixtures';
import { withSettingsLock } from '../settings-lock';
import { loadApp } from './keyboard-helpers';
import { openWorkstreamCard } from './workstream-helpers';

/**
 * Focus around the settings page and the logs pane: each opens with focus in
 * it, and closing gives focus back to where the user was.
 */

test('settings open on the selected category and close back to the list', async ({
  page,
  request,
}) => {
  const { card } = await openWorkstreamCard(
    page,
    request,
    'hs-ui-kbd-panels',
    'kbd-panels',
    0,
  );
  await card.focus();

  await page.keyboard.press('ControlOrMeta+,');
  await expect(
    page
      .getByRole('navigation', { name: 'Settings categories' })
      .locator('[aria-current="true"]'),
  ).toBeFocused();

  await page.getByRole('button', { name: 'Close', exact: true }).focus();
  await page.keyboard.press('Enter');
  await expect(card).toBeFocused();
});

// Nothing here is saved, as a row with no label stays in this browser; the
// lock keeps another test's write from replacing the rows meanwhile.
test('Add entry focuses the new row, and removing it goes back to Add entry', async ({
  page,
}) => {
  await withSettingsLock(async () => {
    await page.goto('/#!/settings/open-with');
    const panel = page.getByRole('region', { name: 'Open with' });
    const add = panel.getByRole('button', { name: 'Add entry' });
    // Enabled once the stored rows have loaded, which replace the draft.
    await expect(add).toBeEnabled();
    await add.focus();
    await page.keyboard.press('Enter');

    const added = panel.getByRole('listitem').last();
    await expect(added.getByRole('textbox').first()).toBeFocused();
    await added.getByRole('button', { name: /^Delete/ }).focus();
    await page.keyboard.press('Enter');
    await expect(add).toBeFocused();
  });
});

test('the logs pane takes focus, and gives it back when it closes', async ({
  page,
}) => {
  await loadApp(page);
  const logsButton = page.getByRole('button', { name: 'Logs' });
  await logsButton.focus();
  await page.keyboard.press('Enter');

  const pane = page.getByRole('region', { name: 'Logs' });
  await expect(pane).toBeFocused();
  await pane.getByRole('button', { name: 'Close logs pane' }).focus();
  await page.keyboard.press('Enter');

  await expect(pane).toHaveCount(0);
  await expect(logsButton).toBeFocused();
});
