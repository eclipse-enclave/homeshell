import { basename } from 'node:path';
import { expect, tempProjectDir, test } from '../fixtures';
import { loadApp, runCommand } from './keyboard-helpers';
import {
  firstSessionPath,
  openWorkstreamCard,
  selectEmptyProject,
} from './workstream-helpers';

/**
 * Where focus goes around creating things: onto what was made once the daemon
 * lists it, back to what opened a form that was cancelled, and never to <body>
 * when a control of the focused row goes away.
 */

test('Escape gives focus back to the card the form was opened from', async ({
  page,
  request,
}) => {
  const { card } = await openWorkstreamCard(
    page,
    request,
    'hs-ui-kbd-focus',
    'kbd-focus',
    0,
  );
  await card.focus();

  await runCommand(page, 'New workstream');
  await expect(
    page.getByRole('form', { name: 'New workstream' }).getByLabel('Name'),
  ).toBeFocused({ timeout: 20_000 });
  await page.keyboard.press('Escape');

  await expect(card).toBeFocused();
});

test('a session created by keyboard ends up focused', async ({
  page,
  request,
}) => {
  const { card } = await openWorkstreamCard(
    page,
    request,
    'hs-ui-kbd-focus',
    'kbd-focus',
    0,
  );

  await card.getByRole('button', { name: 'New session' }).focus();
  await page.keyboard.press('Enter');
  const start = card
    .getByRole('form', { name: 'New session' })
    .getByRole('button', { name: 'Start session' });
  await start.focus();
  await page.keyboard.press('Enter');

  const row = card.getByRole('listitem');
  await expect(row).toBeFocused({ timeout: 20_000 });
  // Still, once the real session has taken the placeholder's place.
  await expect(row.getByTestId('session-state')).not.toHaveText('Starting', {
    timeout: 20_000,
  });
  await expect(row).toBeFocused();
});

test('an added project is selected and focused in the sidebar', async ({
  page,
}) => {
  const dir = await tempProjectDir('hs-ui-kbd-focus-');
  await loadApp(page);
  await page.getByRole('button', { name: 'Add project' }).focus();
  await page.keyboard.press('Enter');
  await expect(page.getByPlaceholder('/path/to/project')).toBeFocused();
  await page.keyboard.type(dir);
  await page.keyboard.press('Enter');

  const entry = page
    .getByRole('navigation', { name: 'Projects' })
    .getByRole('button', { name: new RegExp(basename(dir)) });
  await expect(entry).toBeFocused({ timeout: 20_000 });
  await expect(entry).toHaveAttribute('aria-current', 'true');
});

// Tab moves focus through the sidebar without selecting, so the two must differ.
test('a focused project in the sidebar is marked apart from the selected one', async ({
  page,
  request,
}) => {
  const focused = await selectEmptyProject(page, request, 'hs-ui-kbd-focus-');
  const selected = await selectEmptyProject(page, request, 'hs-ui-kbd-focus-');
  const entry = (name: string) =>
    page
      .getByRole('navigation', { name: 'Projects' })
      .getByRole('button', { name: new RegExp(name) });
  await expect(entry(selected)).toHaveAttribute('aria-current', 'true');

  await page.keyboard.press('Shift');
  await entry(focused).focus();

  await expect(entry(focused)).not.toHaveAttribute('aria-current', 'true');
  await expect(entry(focused)).toHaveCSS('outline-style', 'solid');
  await expect(entry(selected)).toHaveCSS('outline-style', 'none');
});

test('quick open to an empty project focuses its New workstream', async ({
  page,
  request,
}) => {
  const name = await selectEmptyProject(page, request, 'hs-ui-kbd-focus-');
  await page.getByRole('button', { name: 'Settings' }).focus();
  await page.keyboard.press('ControlOrMeta+P');
  await page.keyboard.type(name);
  await page.keyboard.press('Enter');

  await expect(
    page.getByRole('button', { name: 'New workstream' }),
  ).toBeFocused();
});

test('focus goes to the row when a control in it goes away', async ({
  page,
  request,
}) => {
  const { card, row, projectId, workstreamId } = await openWorkstreamCard(
    page,
    request,
    'hs-ui-kbd-focus',
    'kbd-focus',
  );
  await expect(row.getByTestId('session-state')).toHaveText('Idle', {
    timeout: 20_000,
  });
  await row.focus();
  await page.keyboard.press('ArrowRight');
  await card.getByLabel('Send text to session').focus();

  // A stopped session has nothing to send text to, so the box goes.
  const sessionPath = await firstSessionPath(request, projectId, workstreamId);
  expect((await request.post(`${sessionPath}/stop`)).status()).toBe(204);

  await expect(card.getByLabel('Send text to session')).toHaveCount(0);
  await expect(row).toBeFocused();
});

test('Ctrl+Shift+` starts a session in the focused row’s workstream', async ({
  page,
  request,
}) => {
  // One agent and nothing to fill in, so the form opens on Start session.
  await page.route('**/session-schemas', (route) =>
    route.fulfill({
      json: [
        {
          providerId: 'fake',
          label: 'Fake Provider',
          options: [{ id: 'claude', label: 'Claude Code' }],
        },
      ],
    }),
  );
  const { card, row } = await openWorkstreamCard(
    page,
    request,
    'hs-ui-kbd-focus',
    'kbd-focus',
  );
  const rows = card.getByRole('listitem');
  await row.focus();

  // Control on every platform: a Mac binds ⌃⇧` rather than ⇧⌘`.
  await page.keyboard.press('Control+Shift+Backquote');
  await expect(
    card.getByRole('button', { name: 'Start session' }),
  ).toBeFocused();
  await page.keyboard.press('Enter');

  await expect(rows).toHaveCount(2);
  await expect(rows.nth(1)).toBeFocused({ timeout: 20_000 });
});
