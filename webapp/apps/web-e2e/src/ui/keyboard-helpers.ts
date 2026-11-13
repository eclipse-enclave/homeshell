import { type Page } from '@playwright/test';
import { expect } from '../fixtures';

/**
 * Loads the app and waits until it has rendered, and so is listening for
 * keys: a shortcut pressed before that goes nowhere. A helper module, not a
 * spec (see workstream-helpers.ts).
 */
export async function loadApp(page: Page) {
  await page.goto('/');
  await expect(
    page
      .getByRole('navigation', { name: 'Projects' })
      .getByRole('button')
      .first(),
  ).toBeVisible();
}

/** Opens the command palette by F1 and types `query` into it; returns it. */
export async function searchCommands(page: Page, query: string) {
  await page.keyboard.press('F1');
  await page.keyboard.type(query);
  return page.getByRole('dialog', { name: 'Command palette' });
}

/** Opens the log pane by its palette command, and returns the pane. */
export async function openLogs(page: Page) {
  await runCommand(page, 'Toggle logs');
  const pane = page.getByRole('region', { name: 'Logs' });
  await expect(pane).toBeVisible();
  return pane;
}

/** Runs the palette's first command for `query`. */
export async function runCommand(page: Page, query: string) {
  await searchCommands(page, query);
  await page.keyboard.press('Enter');
}

/** What the page reads off the clipboard (the context needs clipboard-read). */
export function readClipboard(page: Page): Promise<string> {
  return page.evaluate(() =>
    (
      navigator as unknown as { clipboard: { readText(): Promise<string> } }
    ).clipboard.readText(),
  );
}
