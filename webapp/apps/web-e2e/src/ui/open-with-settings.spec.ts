import { type Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { settingsSaved, withSettingsLock } from '../settings-lock';
import { styleOf } from './contrast';
import {
  materializedWorkstream,
  selectProject,
  unique,
} from './workstream-helpers';

/**
 * The Open-with settings category, browser to daemon and back. The page's draft
 * of the shared settings document is as old as the page, so every test loads
 * the page and writes inside the settings lock.
 */

test.describe.configure({ mode: 'default' });

async function openCategory(page: Page) {
  await page.goto('/#!/settings/open-with');
  const panel = page.getByRole('region', { name: 'Open with' });
  await expect(panel).toBeVisible();
  return panel;
}

interface SavedEntry {
  label: string;
  command: string;
  inTerminal: boolean;
}

/** The next successful settings write whose Open with rows satisfy `carries`. */
const saved = (page: Page, carries: (entries: SavedEntry[]) => boolean) =>
  settingsSaved<{ openWith?: { entries?: SavedEntry[] } }>(page, (settings) =>
    carries(settings.openWith?.entries ?? []),
  );

/** Adds a row, fills it, and waits for the write carrying both fields. */
async function addRow(
  page: Page,
  panel: ReturnType<Page['getByRole']>,
  label: string,
  command: string,
) {
  await panel.getByRole('button', { name: 'Add entry' }).click();
  const rows = panel
    .getByRole('list', { name: 'Open with entries' })
    .getByRole('listitem');
  const row = rows.last();
  const written = saved(page, (entries) =>
    entries.some((entry) => entry.label === label && entry.command === command),
  );
  await row.getByLabel(/^Label of entry/).fill(label);
  await row.getByLabel(/^Command of entry/).fill(command);
  // Rows commit on blur, so leave the field before waiting for the write.
  await row.getByLabel(/^Command of entry/).blur();
  await written;
}

async function removeRow(
  page: Page,
  panel: ReturnType<Page['getByRole']>,
  label: string,
) {
  const written = saved(
    page,
    (entries) => !entries.some((entry) => entry.label === label),
  );
  await panel.getByRole('button', { name: `Delete ${label}` }).click();
  await written;
}

test('the category ships the default terminal row', async ({ page }) => {
  await withSettingsLock(async () => {
  const panel = await openCategory(page);

  const list = panel.getByRole('list', { name: 'Open with entries' });
  const first = list.getByRole('listitem').first();
  await expect(first.getByLabel(/^Label of entry/)).toHaveValue('Terminal');
  // The command is empty: the daemon resolves this host's terminal at launch.
  await expect(first.getByLabel(/^Command of entry/)).toHaveValue('');
  await expect(first.getByLabel(/in a terminal$/)).toBeChecked();
  });
});

test('a row added in the panel survives a reload', async ({ page }) => {
  await withSettingsLock(async () => {
  const panel = await openCategory(page);
  const label = unique('VSCode');

  await addRow(page, panel, label, 'code .');

  await page.reload();
  const reopened = await openCategory(page);
  const list = reopened.getByRole('list', { name: 'Open with entries' });
  // Rows are located by their Delete button: its accessible name carries the
  // live label, while an input's value is a property, not an attribute.
  const row = list.getByRole('listitem').filter({
    has: page.getByRole('button', { name: `Delete ${label}` }),
  });
  await expect(row.getByLabel(/^Label of entry/)).toHaveValue(label);
  await expect(row.getByLabel(/^Command of entry/)).toHaveValue('code .');
  await expect(row.getByLabel(/in a terminal$/)).not.toBeChecked();

  // Clean up: the daemon is shared.
  await removeRow(page, reopened, label);
  });
});

test('the "in a terminal" statement is the user’s own, and persists', async ({
  page,
}) => {
  await withSettingsLock(async () => {
  const panel = await openCategory(page);
  const label = unique('Neovim');

  await addRow(page, panel, label, 'nvim .');
  const list = panel.getByRole('list', { name: 'Open with entries' });
  const row = list.getByRole('listitem').filter({
    has: page.getByRole('button', { name: `Delete ${label}` }),
  });
  const written = saved(page, (entries) =>
    entries.some((entry) => entry.label === label && entry.inTerminal),
  );
  await row.getByLabel(/in a terminal$/).check();
  await written;

  await page.reload();
  const reopened = await openCategory(page);
  const reloaded = reopened
    .getByRole('list', { name: 'Open with entries' })
    .getByRole('listitem')
    .filter({ has: page.getByRole('button', { name: `Delete ${label}` }) });
  await expect(reloaded.getByLabel(/in a terminal$/)).toBeChecked();

  await removeRow(page, reopened, label);
  });
});

test('a row can be removed, and stays removed', async ({ page }) => {
  await withSettingsLock(async () => {
  const panel = await openCategory(page);
  const label = unique('VIM');

  await addRow(page, panel, label, 'vi .');
  await removeRow(page, panel, label);
  await expect(
    page.getByRole('button', { name: `Delete ${label}` }),
  ).toHaveCount(0);

  await page.reload();
  const reopened = await openCategory(page);
  await expect(
    reopened.getByRole('list', { name: 'Open with entries' }),
  ).toBeVisible();
  await expect(
    page.getByRole('button', { name: `Delete ${label}` }),
  ).toHaveCount(0);
  });
});

test('an added row shows up in a workstream’s Open menu', async ({
  page,
  request,
}) => {
  await withSettingsLock(async () => {
  const { dirName, workstreamName } = await materializedWorkstream(
    request,
    'hs-ui-openwith-',
    'open-menu',
  );
  const panel = await openCategory(page);
  const label = unique('Kitty');

  await addRow(page, panel, label, 'kitty .');

  await page.goto('/');
  await selectProject(page, dirName);
  const card = page.getByRole('region', {
    name: `Workstream ${workstreamName}`,
  });
  await expect(card).toBeVisible();

  // Nothing clicks a menu item: this workstream is materialized, so picking one
  // would launch a real program on whoever runs the suite.
  await card
    .getByRole('button', { name: `Open workstream ${workstreamName} with…` })
    .click();
  const menu = page.getByRole('menu', {
    name: `Open workstream ${workstreamName}`,
  });
  await expect(menu.getByRole('menuitem', { name: label })).toBeVisible();
  await expect(menu.getByRole('menuitem', { name: 'Terminal' })).toBeVisible();
  await page.keyboard.press('Escape');

  // Clean up: the daemon is shared.
  const reopened = await openCategory(page);
  await removeRow(page, reopened, label);
  });
});


test('the default row can be renamed and rows reordered, as the Open menu shows', async ({
  page,
  request,
}) => {
  await withSettingsLock(async () => {
    const before = (await (await request.get('/api/settings')).json()).openWith;
    const { dirName, workstreamName } = await materializedWorkstream(
      request,
      'hs-ui-openwith-',
      'reorder',
    );
    const panel = await openCategory(page);
    const label = unique('Helix');
    const shell = unique('Shell');
    try {
      await addRow(page, panel, label, 'hx .');
      const list = panel.getByRole('list', { name: 'Open with entries' });

      const renamed = saved(page, (entries) => entries[0]?.label === shell);
      const defaultLabel = list.getByRole('listitem').first().getByLabel(/^Label of entry/);
      await defaultLabel.fill(shell);
      await defaultLabel.blur();
      await renamed;

      const moved = saved(page, (entries) => entries[0]?.label === label);
      const up = panel.getByRole('button', { name: `Move ${label} up` });
      const rows = await list.getByRole('listitem').count();
      for (let i = 1; i < rows; i++) await up.click();
      await moved;
      await expect(up).toBeFocused();

      await page.goto('/');
      await selectProject(page, dirName);
      await page
        .getByRole('region', { name: `Workstream ${workstreamName}` })
        .getByRole('button', { name: `Open workstream ${workstreamName} with…` })
        .click();
      const items = page
        .getByRole('menu', { name: `Open workstream ${workstreamName}` })
        .getByRole('menuitem');
      await expect(items.nth(0)).toContainText(label);
      await expect(items.nth(1)).toContainText(shell);
      await page.keyboard.press('Escape');
    } finally {
      const current = await (await request.get('/api/settings')).json();
      await request.put('/api/settings', { data: { ...current, openWith: before } });
    }
  });
});

// Its example command is set off as code, in a monospace face and on a ground
// of its own, so it reads as something to type even without a monospace font.
test('the example command reads as code', async ({ page }) => {
  const panel = await openCategory(page);
  const example = panel.getByRole('code').first();
  await expect(example).toHaveText('vim .');
  expect(await styleOf(example, 'font-family')).toMatch(/monospace/);
  expect(await styleOf(example, 'background-color')).not.toBe(
    await styleOf(example.locator('..'), 'background-color'),
  );
});
