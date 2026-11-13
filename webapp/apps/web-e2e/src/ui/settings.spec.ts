import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { settingsSaved, withSettingsLock } from '../settings-lock';

/**
 * The settings page against the shared daemon. The page PUTs the whole document
 * from a draft as old as the page, so a test that writes holds the settings
 * lock around a freshly-reloaded page.
 *
 * A test that writes also sets up the state it starts from and puts the document
 * back afterwards: the daemon outlives the test, so without that a repeat (or the
 * next spec) starts from whatever this one left.
 */

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

/** The keys these tests set; the rest of the document is carried over as read. */
interface SettingsDoc {
  workstreams: { nameTemplate: string; defaultProvider: { useLastUsed: boolean } };
  providers: { refreshIntervalSec: number };
}

/**
 * Writes the daemon's settings as `change` leaves them and returns the document
 * as it was, for the test to put back. Call it while holding the settings lock.
 */
async function writeSettings(
  request: APIRequestContext,
  change: (doc: SettingsDoc) => void,
): Promise<SettingsDoc> {
  const before = (await (await request.get('/api/settings')).json()) as SettingsDoc;
  const next = structuredClone(before);
  change(next);
  const put = await request.put('/api/settings', { data: next });
  expect(put.status()).toBe(200);
  return before;
}

async function restoreSettings(request: APIRequestContext, doc: SettingsDoc) {
  expect((await request.put('/api/settings', { data: doc })).status()).toBe(200);
}

async function openSettings(page: Page) {
  await page.getByRole('button', { name: 'Settings' }).click();
  await expect(
    page.getByRole('heading', { level: 1, name: 'Settings' }),
  ).toBeVisible();
}

async function openCategory(page: Page, label: string) {
  await openSettings(page);
  await page
    .getByRole('navigation', { name: 'Settings categories' })
    .getByRole('button', { name: label })
    .click();
}

/**
 * Answers this page's settings write instead of letting it reach the daemon,
 * leaving reads alone. Settings there are process-global, so a full-document
 * `PUT` built from what this page loaded can revert a value a concurrent spec
 * has just set (see notify-settings.ts) — while the page itself still behaves
 * as if the write succeeded, which is all a spec about the *controls* needs.
 */
async function stubSettingsWrite(page: Page) {
  await page.route('**/api/settings', async (route) => {
    if (route.request().method() === 'PUT') await route.fulfill({ status: 204 });
    else await route.continue();
  });
}

/**
 * Answers the test notification a style pick asks the daemon for. The daemon
 * broadcasts an in-app one to *every* connected client, so a spec that is not
 * about the test notification itself should not fire real ones into the other
 * specs sharing this daemon.
 */
async function stubTestNotification(page: Page) {
  await page.route('**/api/notifications/test', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ style: 'in-app', channels: [] }),
    }),
  );
}

/** Both writes a Notifications pick makes: the settings PUT and the test. */
async function stubNotificationWrites(page: Page) {
  await stubSettingsWrite(page);
  await stubTestNotification(page);
}

test('opens settings from the toolbar and closes back home', async ({ page }) => {
  await openSettings(page);
  await expect(page).toHaveURL(/#!\/settings$/);

  await page.getByRole('button', { name: 'Close' }).click();
  await expect(
    page.getByRole('heading', { level: 1, name: 'Settings' }),
  ).toHaveCount(0);
  await expect(page.getByRole('navigation', { name: 'Projects' })).toBeVisible();
});

test('every category with something to restore has its own Reset', async ({
  page,
}) => {
  await openSettings(page);
  await expect(page.getByRole('button', { name: 'Reset all' })).toBeVisible();

  const nav = page.getByRole('navigation', { name: 'Settings categories' });
  // Appearance is browser-local and has no defaults to restore; every other
  // category resets its own group, Diagnostics included.
  for (const label of await nav.getByRole('button').allInnerTexts()) {
    await nav.getByRole('button', { name: label, exact: true }).click();
    // `exact` so the header's "Reset all" is not what this matches.
    await expect(
      page.getByRole('button', { name: 'Reset', exact: true }),
    ).toHaveCount(label === 'Appearance' ? 0 : 1);
  }
});

test("a category's Reset restores that category and leaves the others alone", async ({
  page,
}) => {
  // The writes are answered here rather than by the shared daemon: a reset PUTs
  // the whole document, and restoring a group other specs rely on would race
  // them. What this checks is the document the page sends.
  const bodies: Record<string, Record<string, unknown>>[] = [];
  await page.route('**/api/settings', async (route) => {
    if (route.request().method() === 'PUT') {
      bodies.push(route.request().postDataJSON());
      await route.fulfill({ status: 204 });
    } else {
      await route.continue();
    }
  });
  await openCategory(page, 'Workstreams');
  const template = page.getByLabel('Workstream name template');
  await template.fill('{project}-reset-me');
  await expect
    .poll(() => bodies.at(-1)?.workstreams?.nameTemplate)
    .toBe('{project}-reset-me');
  const untouched = bodies.at(-1)?.projects;

  await page.getByRole('button', { name: 'Reset', exact: true }).click();
  await expect(template).not.toHaveValue('{project}-reset-me');
  await expect
    .poll(() => bodies.at(-1)?.workstreams?.nameTemplate)
    .not.toBe('{project}-reset-me');
  expect(bodies.at(-1)?.projects).toEqual(untouched);
});

test('selecting a category updates the URL and survives reload', async ({
  page,
}) => {
  await openCategory(page, 'Providers');
  await expect(page).toHaveURL(/#!\/settings\/providers$/);

  await page.reload();
  await expect(
    page.getByRole('heading', { level: 2, name: 'Providers' }),
  ).toBeVisible();
});

test('changing the theme applies and persists across reload', async ({ page }) => {
  await openSettings(page);
  await page.getByRole('radio', { name: 'Light' }).check();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');

  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
});

test('a configured name template prefills the new-workstream form', async ({
  page,
  request,
}) => {
  const { basename } = await import('node:path');
  const dir = await tempProjectDir('hs-ui-set-');
  const dirName = basename(dir);

  // Held until the form has read the template: another test may set it.
  await withSettingsLock(async () => {
    // Start without a template, so the fill below is an edit the page saves.
    const before = await writeSettings(request, (doc) => {
      doc.workstreams.nameTemplate = '';
    });
    try {
      await page.reload();
      await openCategory(page, 'Workstreams');
      // The write is debounced, so wait for the PUT to land.
      const saved = settingsSaved(page);
      await page.getByLabel('Workstream name template').fill('{project}-ws');
      await saved;
      await page.getByRole('button', { name: 'Close' }).click();

      await page.getByRole('button', { name: 'Add project' }).click();
      await page.getByPlaceholder('/path/to/project').fill(dir);
      await page.getByRole('button', { name: 'Add', exact: true }).click();
      await expect(page.getByPlaceholder('/path/to/project')).not.toBeVisible();

      const nav = page.getByRole('navigation', { name: 'Projects' });
      await nav.getByRole('button', { name: new RegExp(dirName) }).click();
      await page.getByRole('button', { name: 'New workstream' }).click();

      // Pick the fake provider for a deterministic schema: Enclave, where it is
      // installed, adds a "Session name" field that collides with "Name".
      await page.getByRole('radio', { name: 'Fake Provider' }).click();
      await expect(page.getByLabel('Name', { exact: true })).toHaveValue(
        `${dirName}-ws`,
      );
    } finally {
      await restoreSettings(request, before);
    }
  });
});

test('remembering the last selected project restores it after reload', async ({
  page,
}) => {
  const nav = page.getByRole('navigation', { name: 'Projects' });
  // A project that is not first in the list.
  await nav.getByRole('button', { name: /notes/ }).click();
  await expect(
    page.getByRole('heading', { level: 1, name: 'notes' }),
  ).toBeVisible();

  await page.reload();
  await expect(
    page.getByRole('heading', { level: 1, name: 'notes' }),
  ).toBeVisible();
});

test('the refresh interval accepts fractional seconds and clamps below the 0.1 s floor on blur', async ({
  page,
  request,
}) => {
  await withSettingsLock(async () => {
    // Start from a custom interval, so the Reset below always has one to undo.
    const before = await writeSettings(request, (doc) => {
      doc.providers.refreshIntervalSec = 2;
    });
    try {
      await page.reload();
      await openCategory(page, 'Providers');
      const refresh = page.getByLabel('Session-state refresh interval (seconds)');
      await expect(refresh).toBeEnabled();
      await expect(refresh).toHaveValue('2');

      // The category Reset uses the shipped default, not the daemon's interval.
      await page.getByRole('button', { name: 'Reset', exact: true }).click();
      await expect(refresh).toHaveValue('0.5');

      // pressSequentially, not fill: the decimal point must survive keystrokes.
      await refresh.clear();
      await refresh.pressSequentially('0.7');
      await expect(refresh).toHaveValue('0.7');
      await refresh.blur();
      await expect(refresh).toHaveValue('0.7');

      await refresh.fill('0');
      await refresh.blur();
      await expect(refresh).toHaveValue('0.1');

      await refresh.fill('999999999');
      await refresh.blur();
      await expect(refresh).toHaveValue('86400');

      // Back to the shipped default. The commit is debounced, so wait for it.
      const saved = settingsSaved(page);
      await refresh.fill('0.5');
      await refresh.blur();
      await expect(refresh).toHaveValue('0.5');
      await saved;
    } finally {
      await restoreSettings(request, before);
    }
  });
});

test('reflects the daemon log level live under Diagnostics', async ({ page }) => {
  await openCategory(page, 'Diagnostics');
  const select = page.getByLabel('Daemon log level');
  await expect(select).toBeEnabled();
  // The value comes from the live daemon (GET /api/config), not localStorage.
  await expect(select).not.toHaveValue('');
});

test('the Notifications category renders the style, sound and per-event controls', async ({
  page,
}) => {
  // Read-only *against the daemon*: settings are process-global, and a write
  // here would silence (or, with native/both, fire) notifications for the specs
  // sharing this daemon. What the switches *do* is covered by the settings-view
  // unit tests and, end-to-end against a daemon of its own, by
  // api/notification-events.spec.ts.
  //
  // The style is still set *in this page*, because e2e seeds the daemon's style
  // as None and None disables everything below it — without this the test only
  // passes when another spec has happened to turn delivery on first.
  await stubNotificationWrites(page);
  await openCategory(page, 'Notifications');
  await page.getByLabel('Notification style').selectOption('in-app');
  await expect(
    page.getByRole('heading', { level: 2, name: 'Notifications' }),
  ).toBeVisible();

  await expect(page.getByLabel('Notification style')).toBeEnabled();
  await expect(page.getByLabel('Sound effect')).toBeVisible();

  // Each notify-worthy event has its own alert switch plus a sound switch, so
  // "which events" and "which of them are audible" are separately settable.
  await expect(
    page.getByRole('checkbox', { name: 'Needs my input' }),
  ).toBeEnabled();
  await expect(
    page.getByRole('checkbox', { name: 'Finished working' }),
  ).toBeEnabled();
  await expect(page.getByRole('checkbox', { name: 'Play a sound' })).toHaveCount(
    2,
  );
  await expect(
    page.getByLabel('Quiet period for repeats (seconds)'),
  ).toBeEnabled();
});

test('picking a sound effect plays it', async ({ page }) => {
  // The sound preview is a client-side effect, so neither of the writes the
  // panel makes needs to reach the daemon here (see the helpers).
  await stubNotificationWrites(page);

  // Record every tone the page starts. Wrapping the real AudioContext (rather
  // than replacing it) keeps the app's own guards on the code path: what is
  // asserted is that a pick reaches the speakers, not that a stub was called.
  await page.addInitScript(() => {
    // This callback runs in the browser but is type-checked against this
    // project's tsconfig, which has no DOM lib (`lib: ["es2022"]`,
    // `types: node`) — hence globalThis and hand-written shapes, as in
    // session-output.spec.ts.
    const win = globalThis as unknown as {
      __tones: number[];
      AudioContext: {
        prototype: {
          createOscillator(): {
            frequency: { value: number };
            start(when?: number): void;
          };
        };
      };
    };
    const tones: number[] = [];
    win.__tones = tones;
    const proto = win.AudioContext.prototype;
    const create = proto.createOscillator;
    proto.createOscillator = function (this: unknown) {
      const oscillator = create.call(this);
      const start = oscillator.start.bind(oscillator);
      oscillator.start = (when?: number) => {
        tones.push(oscillator.frequency.value);
        start(when);
      };
      return oscillator;
    };
  });
  await page.reload();

  await openCategory(page, 'Notifications');
  // e2e seeds the daemon's style as None (so the demo cycler never fires OS
  // notifications on a developer's machine), and None disables the sound field.
  // Turning delivery on here is a write like any other, so it stops at the
  // routes above: this page gets a deliverable style, the daemon keeps its own.
  const style = page.getByLabel('Notification style');
  await expect(style).toBeEnabled();
  await style.selectOption('in-app');

  const sound = page.getByLabel('Sound effect');
  await expect(sound).toBeEnabled();
  const tones = () =>
    page.evaluate(
      () => (globalThis as unknown as { __tones: number[] }).__tones,
    );

  // Asserted by pitch rather than by count: this page is a live client of a
  // shared daemon, so an in-app notification (the test one the style pick
  // above sends, the demo cycler's, or another spec's) can play a tone of its
  // own at any moment, including after a baseline was taken. That a pick is
  // the *only* thing that previews is a client-side rule, asserted in the unit
  // tests. The pitches are notification-sound.ts's, so the preview is seen to
  // follow the pick.
  const GLASS = 1319;
  const CHIME = 1047;
  const before = (await tones()).length;
  const since = async () => (await tones()).slice(before);

  await sound.selectOption('glass');
  await expect.poll(since).toContain(GLASS);
  await sound.selectOption('chime');
  await expect.poll(since).toContain(CHIME);

  // That "Silent" previews silence is asserted in notification-sound.spec.ts:
  // a tone count is not a safe way to prove a negative on a live page.
  await sound.selectOption('none');
  await expect(sound).toHaveValue('none');
});

test('picking the in-app style shows a test notification', async ({ page }) => {
  // The settings write is answered locally (the daemon's own style must stay
  // None for the rest of the suite), but the test notification itself goes to
  // the daemon for real: it is delivered over SSE and rendered here, which is
  // the whole behavior under test. Only the in-app style is picked, so nothing
  // reaches the developer's desktop.
  await stubSettingsWrite(page);

  await openCategory(page, 'Notifications');
  const style = page.getByLabel('Notification style');
  await expect(style).toBeEnabled();

  const sample = page
    .getByRole('region', { name: 'Notifications' })
    .getByRole('alert')
    .filter({ hasText: 'Test notification' });
  await expect(sample).toHaveCount(0);

  await style.selectOption('in-app');
  // The daemon authors and delivers it; the settings page just asked for it.
  await expect(sample.first()).toBeVisible({ timeout: 15_000 });
});

test('the "Last used" checkbox disables the value control', async ({
  page,
  request,
}) => {
  await withSettingsLock(async () => {
    // Start with "Last used" off, so checking it is an edit the page saves.
    const before = await writeSettings(request, (doc) => {
      doc.workstreams.defaultProvider.useLastUsed = false;
    });
    try {
      await page.reload();
      await openCategory(page, 'Workstreams');
      const providerSelect = page.getByLabel('Default provider');
      await expect(providerSelect).toBeEnabled();

      // The first "Last used" checkbox belongs to the default provider field.
      // The write is debounced, so the lock is held until the PUT has landed.
      const saved = settingsSaved(page);
      await page.getByRole('checkbox', { name: /Last used/ }).first().check();
      await expect(providerSelect).toBeDisabled();
      await saved;
    } finally {
      await restoreSettings(request, before);
    }
  });
});
