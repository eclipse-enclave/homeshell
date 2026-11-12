import { basename } from 'node:path';
import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { controlEventStream } from './event-stream';
import { openLogs } from './keyboard-helpers';
import {
  openMoreActions,
  selectProject,
  sessionRows,
} from './workstream-helpers';
import { addEmptyProject } from '../projects';
import { createSession, createWorkstream } from '../api/workstream-helpers';

/**
 * What the UI does when it cannot reach the daemon: every action that would call
 * it goes **disabled** — visibly present, but dead — while the state already on
 * screen stays, and the purely local controls keep working.
 *
 * The disconnect is produced by intercepting the **event stream** (`/api/events`)
 * for this page only, rather than by stopping the daemon the whole suite shares.
 * The interception is not cosmetic: the client decides it is offline from an
 * `EventSource` *error*, so the spec has to make the stream actually fail. Note
 * that `context.setOffline(true)` does **not** work here — Chromium's network
 * emulation leaves an established loopback connection alone, so the browser reports
 * no error and the client keeps showing itself connected.
 *
 * Only `/api/events` is intercepted, so every other request still reaches the real
 * daemon. That matters for the settings case: its `GET /api/settings` succeeds, so
 * the fields being read-only is attributable to the connection and not to a failed
 * settings load, which disables them for its own reason.
 */

/** How the intercepted stream behaves; flipped mid-test. */
type StreamMode = 'seed' | 'block' | 'live';

async function projectWithSession(
  request: APIRequestContext,
): Promise<{ dirName: string }> {
  const dir = await tempProjectDir('hs-ui-offline-');
  const projectId = await addEmptyProject(request, dir);

  const workstreamId = await createWorkstream(request, projectId, 'ui-offline');
  // The real session, not its placeholder: a starting placeholder deliberately
  // offers no per-session actions to assert on.
  await createSession(request, projectId, workstreamId);
  return { dirName: basename(dir) };
}

/**
 * Takes over `/api/events` for this page. Returns a setter for the mode:
 *
 * - `seed` — answer once with a complete, then-ended SSE body carrying the
 *   daemon's current projection. The client renders it and counts as connected;
 *   the stream ending is what the browser reports as an error, exactly as it would
 *   if the daemon went away mid-stream.
 * - `block` — fail the request, so every reconnect attempt keeps it offline.
 * - `live` — hand the request to the real daemon, so the client recovers for real.
 */
async function interceptEvents(
  page: Page,
  snapshot: unknown,
): Promise<(mode: StreamMode) => void> {
  let mode: StreamMode = 'seed';
  await page.route('**/api/events**', async (route) => {
    if (mode === 'live') return route.continue();
    if (mode === 'block') return route.abort('failed');
    // One snapshot, then end the body — the client is connected, then dropped.
    mode = 'block';
    await route.fulfill({
      status: 200,
      headers: {
        'content-type': 'text/event-stream',
        'cache-control': 'no-cache',
      },
      body: `event: state\ndata: ${JSON.stringify(snapshot)}\n\n`,
    });
  });
  return (next: StreamMode) => {
    mode = next;
  };
}

/** Selects the project in `dirName`, and returns its workstream's card once shown. */
async function showCard(page: Page, dirName: string) {
  await selectProject(page, dirName);
  const card = page.getByRole('region', { name: 'Workstream ui-offline' });
  await expect(card).toBeVisible();
  return card;
}

test('every daemon-backed action goes disabled while the daemon is unreachable', async ({
  page,
  request,
}) => {
  const { dirName } = await projectWithSession(request);
  const snapshot = await (await request.get('/api/projects')).json();
  const setMode = await interceptEvents(page, snapshot);

  await page.goto('/');
  const card = await showCard(page, dirName);

  // The seeded stream has already ended, so the client is offline by now. The
  // toolbar reports it — the one signal the user gets, by design.
  await expect(page.getByText('daemon offline')).toBeVisible({
    timeout: 20_000,
  });

  // Project-level actions.
  for (const name of [
    'Add project',
    'New workstream',
    `Delete project ${dirName}`,
  ]) {
    await expect(
      page.getByRole('button', { name, exact: true }),
    ).toBeDisabled();
  }
  // The project's name, which is what renames it, says it cannot, and neither a
  // double-click on it nor Alt+R opens the box.
  const title = page.getByRole('heading', { level: 1, name: dirName });
  await expect(title).toHaveAttribute('title', /daemon/i);
  await expect(title).not.toHaveAttribute('data-mnemonic');
  await title.dblclick();
  await page.keyboard.press('Alt+r');
  await expect(page.getByRole('textbox', { name: 'Project name' })).toHaveCount(
    0,
  );
  // Nor does the card's menu offer a rename.
  await card.getByRole('heading', { name: 'ui-offline' }).click({
    button: 'right',
  });
  await expect(
    page.getByRole('menuitem', { name: 'Rename', disabled: false }),
  ).toHaveCount(0);
  await page.keyboard.press('Escape');
  // Workstream- and session-level actions.
  for (const name of [
    /^New session$/,
    /^Delete workstream/,
    // The Open control. The fake never creates the workstream's folder, so there
    // is no default open and the control is the modes menu alone; the split
    // button's two halves are covered by the card's unit test.
    /^Open workstream [^ ]+$/,
  ]) {
    await expect(card.getByRole('button', { name })).toBeDisabled();
  }
  // The session's menu leaves out what cannot run.
  await openMoreActions(sessionRows(card).first());
  await expect(page.getByRole('menuitem', { name: 'Delete' })).toHaveCount(0);
  await page.keyboard.press('Escape');
  // Text boxes go with the buttons: they exist to feed a request. The session's
  // send-text box is the one reachable without opening a form.
  await card
    .getByRole('button', { name: 'Toggle session details' })
    .first()
    .click();
  await expect(card.getByLabel('Send text to session')).toBeDisabled();

  // Rename has no button to disable — the gesture on the name has to be inert.
  // Asserted after the box above, because a double-click's first click also
  // toggles the details panel (a click on the row does, by design), which would
  // close it.
  await card.getByTestId('session-title').dblclick();
  await expect(card.getByLabel('Session name')).toHaveCount(0);

  // The state that was already on screen is still there: it is the last thing
  // known to be true, and it is what the user looks at while waiting.
  await expect(card.getByTestId('workstream-sessions')).toHaveAttribute(
    'aria-label',
    /^Sessions: /,
  );

  // Local controls keep working — the log pane especially, since a dead daemon is
  // exactly when someone opens it.
  await expect(page.getByRole('button', { name: 'Settings' })).toBeEnabled();
  await openLogs(page);
  await expect(
    page.getByRole('button', { name: 'Close logs pane' }),
  ).toBeVisible();
  await page.getByRole('button', { name: 'Close logs pane' }).click();

  // And it all comes back on its own once the daemon is reachable again — no
  // reload, no user action.
  setMode('live');
  await expect(
    page.getByRole('button', { name: 'New workstream' }),
  ).toBeEnabled({ timeout: 30_000 });
  await expect(
    page
      .getByRole('region', { name: 'Workstream ui-offline' })
      .getByRole('button', { name: 'New session' }),
  ).toBeEnabled({ timeout: 30_000 });
});

test('the settings page stops accepting daemon-backed edits while offline', async ({
  page,
}) => {
  // No projection needed here, so the stream is refused outright.
  await page.route('**/api/events**', (route) => route.abort('failed'));

  await page.goto('/#!/settings/workstreams');
  await expect(page.getByText('daemon offline')).toBeVisible({
    timeout: 20_000,
  });

  // Daemon-stored: read-only while it cannot be written. `GET /api/settings`
  // succeeded (only the stream is blocked), so this is the connection's doing.
  await expect(page.getByLabel('Workstream name template')).toBeDisabled();

  // The reason is on screen, not only in a tooltip: the load-failure line beside
  // it is visible, and a disabled control has to carry its reason.
  await expect(
    page
      .getByRole('alert')
      .filter({ hasText: /daemon/i })
      .first(),
  ).toBeVisible();

  // Reset all PUTs the whole document, so it is a daemon write like the fields
  // above and goes with them.
  await expect(page.getByRole('button', { name: 'Reset all' })).toBeDisabled();

  // Appearance is browser-local, so it is unaffected by a missing daemon.
  await page.getByRole('button', { name: 'Appearance' }).click();
  await expect(page.getByRole('radio', { name: 'Dark' })).toBeEnabled();

  // Diagnostics spans both worlds: the log level is a daemon call, the log
  // buffer size a localStorage write. The local half has to keep working while
  // the daemon half stays off the wire.
  let patchedConfig = false;
  await page.route('**/api/config**', async (route) => {
    if (route.request().method() === 'PATCH') patchedConfig = true;
    // Continued, not aborted: `GET /api/config` still loads the level.
    await route.continue();
  });
  await page.getByRole('button', { name: 'Diagnostics' }).click();

  // The log buffer size is a localStorage write, so it stays editable even
  // though the reset that would also have restored it is a daemon write and is
  // off. Nothing here is unreachable offline — it just takes the field.
  const buffer = page.getByLabel('Log buffer size (entries)');
  await expect(buffer).toBeEnabled();
  await buffer.fill('2500');
  await expect(buffer).toHaveValue('2500');

  // And the daemon half of Diagnostics never leaves the browser.
  expect(patchedConfig).toBe(false);
});

// Renaming is the one control that must not simply go disabled: disabling a
// focused input blurs it, and the blur is what commits, so the request this whole
// feature exists to stop would fire on the way down — stranding the user in a dead
// box holding unsaved text. The box stays live and the commit is refused instead.
test('a rename in flight when the daemon drops is refused, not fired', async ({
  page,
  request,
}) => {
  const { dirName } = await projectWithSession(request);
  const snapshot = await (await request.get('/api/projects')).json();
  await interceptEvents(page, snapshot);

  await page.goto('/');
  await showCard(page, dirName);
  await expect(page.getByText('daemon offline')).toBeVisible({
    timeout: 20_000,
  });

  // A PATCH must not leave the browser at all while it is…
  let patched = false;
  await page.route('**/api/projects/**', async (route) => {
    if (route.request().method() === 'PATCH') patched = true;
    await route.abort('failed');
  });

  // …whichever way in is tried: the name says it cannot be renamed, and a
  // double-click on it, Alt+R, and F2 on a card open no box to commit.
  const title = page.getByRole('heading', { level: 1, name: dirName });
  await expect(title).toHaveAttribute('title', /daemon/i);
  await expect(title).not.toHaveAttribute('data-mnemonic');
  await title.dblclick();
  await page.keyboard.press('Alt+r');
  const card = page.getByRole('region', { name: 'Workstream ui-offline' });
  await card.getByRole('heading', { name: 'ui-offline' }).dblclick();
  await card.focus();
  await page.keyboard.press('F2');
  await expect(page.getByRole('textbox')).toHaveCount(0);
  await page.keyboard.press('Enter');
  await page.waitForTimeout(250);
  expect(patched).toBe(false);
});

// Enter asks for focus back once the box closes, but offline the box stays
// open; the blur that later commits it must not pull focus back to it.
test.describe('a rename refused offline takes no focus back when it later commits', () => {
  for (const where of ['project', 'workstream'] as const) {
    test(`of the ${where}`, async ({ page, request }) => {
      const { dirName } = await projectWithSession(request);
      const stream = await controlEventStream(page);
      await page.goto('/');
      const card = await showCard(page, dirName);
      const status = page.getByText('daemon offline');
      const elsewhere = page.getByRole('button', {
        name: /^Switch to .* theme$/,
      });

      const [title, box, error, renamed] =
        where === 'project'
          ? [
              page.getByRole('heading', { level: 1, name: dirName }),
              page.getByRole('textbox', { name: 'Project name' }),
              page.getByRole('main').getByRole('alert'),
              page.getByRole('heading', {
                level: 1,
                name: `${dirName}-renamed`,
              }),
            ]
          : [
              card.getByRole('heading', { name: 'ui-offline' }),
              card.getByRole('textbox', { name: 'Workstream name' }),
              card.getByRole('alert'),
              page.getByRole('region', {
                name: 'Workstream ui-offline-renamed',
              }),
            ];
      await title.dblclick();
      await box.press('End');
      await page.keyboard.type('-renamed');
      await stream.drop();
      await expect(status).toBeVisible();
      await page.keyboard.press('Enter');
      await expect(error).toBeVisible();
      await expect(box).toBeFocused();

      await stream.restore();
      await expect(status).toBeHidden({ timeout: 20_000 });
      await elsewhere.click();
      await expect(renamed).toBeVisible();
      await expect(box).toHaveCount(0);
      await expect(elsewhere).toBeFocused();
    });
  }
});
