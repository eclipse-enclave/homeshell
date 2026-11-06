import { expect, test, type Page, type Locator } from '@playwright/test';

/**
 * The log viewer is a dockable bottom pane inside the app (not a separate
 * window), so the main UI stays usable while logs accumulate. To avoid changing
 * the process-global log level (which the config tests exercise in parallel),
 * these tests generate a Warn-level record with a 4xx request, which streams at
 * the daemon's default level.
 */

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

async function openLogPane(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: 'Logs' }).click();
  const pane = page.getByRole('region', { name: 'Logs' });
  await expect(pane).toBeVisible();
  return pane;
}

test('opens and closes the log pane from the toolbar', async ({ page }) => {
  const pane = await openLogPane(page);
  // The level control reflects the live daemon level.
  await expect(pane.getByLabel('Level')).not.toHaveValue('');
  // The main app stays present alongside the pane (not a separate page).
  await expect(page.getByRole('navigation', { name: 'Projects' })).toBeVisible();

  await pane.getByRole('button', { name: 'Close logs pane' }).click();
  await expect(page.getByRole('region', { name: 'Logs' })).toHaveCount(0);
});

test('streams a log entry into the pane', async ({ page }) => {
  const pane = await openLogPane(page);
  await expect(pane.getByRole('status')).toHaveText('live');

  // A 4xx request is logged at Warn and streams at the default level.
  const bad = await page.request.get('/api/__no_such_route__');
  expect(bad.status()).toBe(404);

  // The record reaches the pane's live list. (Clicking a row to reveal its
  // details is covered deterministically by the LogViewer unit test — asserting
  // it here is flaky because the list auto-scrolls under the shared daemon's
  // ongoing log traffic, so the click target never settles.)
  const stream = pane.getByRole('log', { name: 'Log entries' });
  await expect(stream.getByRole('button').first()).toBeVisible({ timeout: 15_000 });
  await expect(stream.getByText('api request').first()).toBeVisible();
});

test('resizes the pane by dragging the sash', async ({ page }) => {
  const pane = await openLogPane(page);
  const before = (await pane.boundingBox())?.height ?? 0;
  const sash = pane.getByRole('separator', { name: 'Resize logs pane' });
  const box = await sash.boundingBox();
  if (!box) throw new Error('no sash');
  // Drag the sash up by 80px to grow the pane.
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2, box.y - 80);
  await page.mouse.up();
  const after = (await pane.boundingBox())?.height ?? 0;
  expect(after).toBeGreaterThan(before);
});

test('copies streamed log entries to the clipboard', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const pane = await openLogPane(page);
  await expect(pane.getByRole('status')).toHaveText('live');

  const bad = await page.request.get('/api/__no_such_route__');
  expect(bad.status()).toBe(404);

  const stream = pane.getByRole('log', { name: 'Log entries' });
  await expect(stream.getByRole('button').first()).toBeVisible({ timeout: 15_000 });

  await pane.getByRole('button', { name: /^Copy/ }).click();
  const text = await page.evaluate(() =>
    (
      navigator as unknown as { clipboard: { readText(): Promise<string> } }
    ).clipboard.readText()
  );
  expect(text).toContain('api request');
});
