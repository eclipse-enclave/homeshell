import type { Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { settingsSaved, withSettingsLock } from '../settings-lock';

/**
 * The Terminal settings category, browser to daemon and back. The page's draft
 * of the shared settings document is as old as the page, so every test loads
 * the page and writes inside the settings lock, and puts the default back.
 */

interface TerminalChoice {
  emulator: string;
  command: string;
}

/** The next successful settings write whose terminal choice satisfies `carries`. */
const saved = (page: Page, carries: (terminal: TerminalChoice) => boolean) =>
  settingsSaved<{ terminal: TerminalChoice }>(page, (settings) =>
    carries(settings.terminal),
  );

async function openCategory(page: Page) {
  await page.goto('/#!/settings/terminal');
  const select = page.getByLabel('Terminal emulator');
  // The daemon's list has arrived once more than Automatic and Custom are offered.
  await expect.poll(() => select.getByRole('option').count()).toBeGreaterThan(2);
  return select;
}

async function restoreAuto(page: Page) {
  // Emptied first, so no template is left stored; an empty custom one stays local.
  const field = page.getByLabel('Custom terminal command');
  if (await field.isVisible()) await field.fill('');
  const restored = saved(
    page,
    (terminal) => terminal.emulator === 'auto' && terminal.command === '',
  );
  await page.getByLabel('Terminal emulator').selectOption('auto');
  await restored;
}

test('choosing an emulator persists across a reload', async ({ page }) => {
  await withSettingsLock(async () => {
    const select = await openCategory(page);
    try {
      // The first emulator the daemon lists, whatever the platform.
      const emulator = await select.getByRole('option').nth(1).getAttribute('value');
      const written = saved(page, (terminal) => terminal.emulator === emulator);
      await select.selectOption(emulator ?? '');
      await written;

      await page.reload();
      await expect(page.getByLabel('Terminal emulator')).toHaveValue(emulator ?? '');
    } finally {
      await restoreAuto(page);
    }
  });
});

test('a custom command is saved once it includes {command}', async ({ page }) => {
  await withSettingsLock(async () => {
    const select = await openCategory(page);
    try {
      await select.selectOption('custom');
      const field = page.getByLabel('Custom terminal command');
      await field.fill('foot -e');
      await expect(page.getByText('Not saved until it includes {command}.')).toBeVisible();

      const written = saved(
        page,
        (terminal) =>
          terminal.emulator === 'custom' && terminal.command === 'foot -e {command}',
      );
      await field.fill('foot -e {command}');
      await written;

      await page.reload();
      await expect(page.getByLabel('Custom terminal command')).toHaveValue(
        'foot -e {command}',
      );
    } finally {
      await restoreAuto(page);
    }
  });
});

test('Automatic names the emulator it opens windows in', async ({
  page,
  request,
}) => {
  const { automatic } = await (
    await request.get('/api/terminal-emulators')
  ).json();
  const select = await openCategory(page);

  await expect(select.locator('option[value="auto"]')).toHaveText(
    automatic ? `Automatic (${automatic})` : 'Automatic (none installed)',
  );
});

const noneListedInstalled = [
  { id: 'ghostty', label: 'Ghostty', installed: false },
  { id: 'kitty', label: 'kitty', installed: false },
];

test('Automatic says when it has nothing to open', async ({ page }) => {
  await page.route('**/api/terminal-emulators', (route) =>
    route.fulfill({
      json: { automatic: '', emulators: noneListedInstalled },
    }),
  );
  const select = await openCategory(page);

  await expect(
    select.getByRole('option', { name: 'Automatic (none installed)' }),
  ).toBeAttached();
  await expect(
    page.getByText(/None of the emulators listed here is installed/),
  ).toBeVisible();
});

// $TERMINAL can name an emulator the list does not know, which Automatic still
// opens: that is not none installed.
test('Automatic names a $TERMINAL the list does not know', async ({ page }) => {
  await page.route('**/api/terminal-emulators', (route) =>
    route.fulfill({
      json: { automatic: 'st', emulators: noneListedInstalled },
    }),
  );
  const select = await openCategory(page);

  await expect(
    select.getByRole('option', { name: 'Automatic (st)' }),
  ).toBeAttached();
  await expect(
    page.getByText(/None of the emulators listed here is installed/),
  ).toHaveCount(0);
});

test('a list the daemon cannot give is reported, not left out', async ({
  page,
}) => {
  await page.route('**/api/terminal-emulators', (route) =>
    route.abort('failed'),
  );
  await page.goto('/#!/settings/terminal');

  // Not the only alert: the page says it is connecting until the daemon answers.
  await expect(
    page.getByRole('alert').filter({ hasText: 'Could not load the emulators' }),
  ).toHaveText(
    'Could not load the emulators from the daemon, so only Automatic and Custom are listed.',
  );
  await expect(
    page.getByLabel('Terminal emulator').getByRole('option'),
  ).toHaveText(['Automatic', 'Custom command']);
});
