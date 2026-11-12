import { expect, test } from '../fixtures';
import { loadApp } from './keyboard-helpers';

/** The top bar holds two icon buttons; the log pane is reached from the palette. */

test('the top bar has theme and settings icon buttons, and no Logs button', async ({
  page,
}) => {
  await loadApp(page);
  const bar = page.getByRole('banner');
  for (const name of [/^Switch to (light|dark) theme$/, 'Settings']) {
    const button = bar.getByRole('button', { name });
    await expect(button).toBeVisible();
    await expect(button.locator('svg')).toHaveCount(1);
  }
  await expect(bar.getByRole('button', { name: 'Logs' })).toHaveCount(0);
});
