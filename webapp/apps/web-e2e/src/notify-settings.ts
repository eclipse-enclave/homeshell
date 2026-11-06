import type { APIRequestContext } from '@playwright/test';
import { withSettingsLock } from './settings-lock';

/**
 * Sets `notifications.style` on the shared daemon via a full-document PUT —
 * GET the current settings, swap the style, PUT it back.
 *
 * The rest of the notifications group is carried over rather than rewritten:
 * PUT replaces the whole document, so a hard-coded `{style, sound}` would reset
 * every other notification field to its zero value — which for the per-event
 * switches means "notify: false", silencing the very channel this helper exists
 * to open.
 *
 * Callers only ever write `in-app`: it rides SSE and never reaches the OS, so on
 * this shared daemon no spec silences a channel another is reading.
 */
export async function setNotificationStyle(
  request: APIRequestContext,
  style: 'none' | 'in-app' | 'native' | 'both',
): Promise<void> {
  // The daemon is shared, so the GET-modify-PUT holds the settings lock; the
  // verify-and-retry backstops races the lock does not cover.
  for (let attempt = 0; attempt < 10; attempt++) {
    const landed = await withSettingsLock(async () => {
      const current = await (await request.get('/api/settings')).json();
      current.notifications = { ...current.notifications, style };
      await request.put('/api/settings', { data: current });
      const stored = await (await request.get('/api/settings')).json();
      return stored.notifications?.style === style;
    });
    if (landed) return;
  }
  throw new Error(`notifications.style did not settle at ${style}`);
}
