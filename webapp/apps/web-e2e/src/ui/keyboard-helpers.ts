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

/** Runs the palette's first command for `query`. */
export async function runCommand(page: Page, query: string) {
  await searchCommands(page, query);
  await page.keyboard.press('Enter');
}
