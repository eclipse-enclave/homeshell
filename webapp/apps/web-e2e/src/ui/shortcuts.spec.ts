import { basename } from 'node:path';
import { expect, tempProjectDir, test } from '../fixtures';
import { loadApp } from './keyboard-helpers';
import { selectProject, unique, waitForRealCard } from './workstream-helpers';
import { addEmptyProject } from '../projects';

/** The shortcut reference: Ctrl/Cmd+K Ctrl/Cmd+S, as in VS Code, or `?`. */

test('the Ctrl/Cmd+K Ctrl/Cmd+S chord shows the shortcut reference', async ({
  page,
}) => {
  await loadApp(page);
  await page.keyboard.press('ControlOrMeta+K');
  await page.keyboard.press('ControlOrMeta+S');

  const dialog = page.getByRole('dialog', { name: 'Keyboard shortcuts' });
  await expect(dialog).toBeVisible();
  const anywhere = dialog.getByRole('table', { name: 'Anywhere' });
  await expect(
    anywhere.getByRole('row', { name: /Show all commands/ }),
  ).toContainText('F1');
  // Listed by a fixed title, not by what the palette's command would act on.
  await expect(
    anywhere.getByRole('row', {
      name: /New session in the selected workstream/,
    }),
  ).toContainText('Ctrl+Shift+`');
  await expect(
    anywhere.getByRole('row', { name: /list the commands instead/ }),
  ).toContainText('>');
  await expect(
    dialog
      .getByRole('table', { name: 'In the workstream list' })
      .getByRole('row', { name: /Rename the focused workstream/ }),
  ).toContainText('F2');
  await expect(
    dialog
      .getByRole('table', { name: 'In the folder picker' })
      .getByRole('row', { name: /Select the folder shown/ }),
  ).toContainText('Enter');

  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
});

test('? shows it too, but not while typing', async ({ page }) => {
  await loadApp(page);
  await page.getByRole('button', { name: 'Add project' }).click();
  await page.keyboard.type('what?');
  await expect(page.getByRole('dialog')).toHaveCount(0);

  await page.getByRole('button', { name: 'Cancel' }).click();
  await page.keyboard.press('?');
  await expect(
    page.getByRole('dialog', { name: 'Keyboard shortcuts' }),
  ).toBeVisible();
});

// The menu item it was opened from closes with the menu, so focus goes to the
// list instead of falling to <body>.
test('closing it after an actions menu gives focus back to the list', async ({
  page,
  request,
}) => {
  const dir = await tempProjectDir('hs-ui-shortcuts-');
  const projectId = await addEmptyProject(request, dir);
  const name = unique('shortcuts');
  const workstream = await request.post(
    `/api/projects/${projectId}/workstreams`,
    { data: { providerId: 'fake', name, config: { branch: 'main' } } },
  );
  expect(workstream.status()).toBe(201);

  await loadApp(page);
  await selectProject(page, basename(dir));
  const card = page.getByRole('region', { name: `Workstream ${name}` });
  await waitForRealCard(card);
  await card.focus();
  await page.keyboard.press('Shift+F10');
  await expect(
    page.getByRole('menu', { name: `Actions for workstream ${name}` }),
  ).toBeVisible();

  await page.keyboard.press('?');
  const dialog = page.getByRole('dialog', { name: 'Keyboard shortcuts' });
  await expect(dialog).toBeVisible();
  await expect(page.getByRole('menu')).toHaveCount(0);
  await page.keyboard.press('Escape');

  await expect(dialog).toHaveCount(0);
  await expect(card).toBeFocused();
});
