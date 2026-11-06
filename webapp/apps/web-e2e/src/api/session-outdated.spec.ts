import { expect, test } from '@playwright/test';
import { pollFor, sessionsOf } from './workstream-helpers';

/**
 * A session whose provider config changed since it started names the settings
 * a restart would change. The demo seeds one such session.
 */
test('a session lists the settings that changed since it started', async ({
  request,
}) => {
  const outdated = await pollFor(
    'the seeded out-of-date session',
    async () =>
      (await sessionsOf(request, 'proj-webshop', 'ws-shop-tax')).find(
        (session) => session.id === 'sess-shop-tests',
      ),
    5000,
  );
  expect(outdated.outdatedSettings).toEqual(['allow_domains', 'features']);

  const upToDate = (
    await sessionsOf(request, 'proj-webshop', 'ws-shop-tax')
  ).find((session) => session.id === 'sess-shop-fix');
  expect(upToDate?.outdatedSettings).toBeUndefined();
});
