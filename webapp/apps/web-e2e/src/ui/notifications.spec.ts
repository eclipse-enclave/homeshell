import { expect, test } from '../fixtures';
import { setNotificationStyle } from '../notify-settings';

/**
 * The web app shows an in-app toast (the "UI notification" of issue #66) when
 * the daemon pushes a notification over SSE. e2e keeps notifications disabled
 * by default so the demo cycler never fires real OS-native ones; each test opts
 * the (process-global) daemon into the in-app style — SSE only, never the OS.
 * It deliberately never writes `none`/`native`/`both`, so the channel a
 * concurrent test may be reading is never silenced and no OS notification ever
 * fires. The demo simulator walks a session through waiting-for-input and
 * finishing every cycle, so a toast appears without any further action. The
 * toast is global (rendered at the app root), so it shows regardless of which
 * project is selected.
 *
 * Real OS-native notifications are shown by the daemon and are deliberately not
 * exercised here; this covers the in-app path, with the api suite covering the
 * daemon's SSE contract and unit tests covering toast rendering.
 */
test('shows a toast when a session waits for input', async ({ page, request }) => {
  test.setTimeout(60_000);
  await setNotificationStyle(request, 'in-app');
  await page.goto('/');

  const toast = page
    .getByRole('region', { name: 'Notifications' })
    .getByRole('alert')
    .filter({ hasText: /waiting for input/i })
    .first();

  await expect(toast).toBeVisible({ timeout: 40_000 });
});

test('a notification toast can be dismissed', async ({ page, request }) => {
  test.setTimeout(60_000);
  await setNotificationStyle(request, 'in-app');
  await page.goto('/');

  const region = page.getByRole('region', { name: 'Notifications' });
  const firstToast = region.getByRole('alert').first();
  await expect(firstToast).toBeVisible({ timeout: 40_000 });

  // Target this specific toast by its title (unique per demo cycle) so a later
  // cycle's toast can't be mistaken for it after dismissal.
  const title = (await firstToast.getByText(/waiting for input|finished/i).textContent())!;
  const thisToast = region.getByRole('alert').filter({ hasText: title });

  await thisToast.getByRole('button', { name: 'Dismiss notification' }).click();
  await expect(thisToast).toHaveCount(0);
});
