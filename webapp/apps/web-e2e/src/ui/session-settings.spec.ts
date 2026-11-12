import { tmpdir } from 'node:os';
import {
  type APIRequestContext,
  type Locator,
  type Page,
} from '@playwright/test';
import { expect, test } from '../fixtures';
import { pollFor, sessionsOf } from '../api/workstream-helpers';
import { loadApp } from './keyboard-helpers';
import {
  selectProject,
  waitForRealCard,
  workstreamWithSessions,
} from './workstream-helpers';

/**
 * A session option's nested settings — the fake's "Sandboxed demo" serves the
 * Enclave provider's container group — in the new-session form, collapsed to
 * a summary, and in the dialog that restarts a session with new settings.
 * What reached the daemon is read back from its launch-config record.
 */

const OPTION = 'Sandboxed demo';

/** A fresh, empty fake workstream, on screen; returns its card and IDs. */
async function emptyCard(page: Page, request: APIRequestContext) {
  const created = await workstreamWithSessions(
    request,
    'hs-ui-launch',
    'settings',
    0,
  );
  await loadApp(page);
  await selectProject(page, created.projectPrefix);
  const card = page.getByRole('region', {
    name: `Workstream ${created.workstreamName}`,
  });
  await waitForRealCard(card);
  return { ...created, card };
}

/** Opens the card's new-session form on the sandboxed option. */
async function openSandboxedForm(page: Page, card: ReturnType<Page['locator']>) {
  await card.getByRole('button', { name: 'New session' }).click();
  const form = page.getByRole('form', { name: 'New session' });
  await form.getByRole('radio', { name: OPTION }).click();
  return form;
}

/** The launch config of the workstream's one session, once it has one. */
async function launchConfigOf(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
) {
  const session = await pollFor('the session', async () => {
    const [listed] = await sessionsOf(request, projectId, workstreamId);
    return listed && !listed.id.startsWith('starting-') ? listed : undefined;
  }, 20_000);
  const path = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${session.id}`;
  const res = await request.get(`${path}/launch-config`);
  expect(res.status()).toBe(200);
  return { path, launched: await res.json() };
}

/**
 * A fresh workstream holding one sandboxed session started with `config`, on
 * screen; returns its row and the session's API path.
 */
async function sandboxedSession(
  page: Page,
  request: APIRequestContext,
  config: Record<string, unknown> = {},
) {
  const created = await emptyCard(page, request);
  const { projectId, workstreamId, card } = created;
  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'sandboxed', config } },
  );
  expect(res.ok()).toBe(true);
  const { path, launched } = await launchConfigOf(
    request,
    projectId,
    workstreamId,
  );
  const row = card.getByRole('listitem').first();
  await expect(row.getByTestId('session-title')).toHaveText(OPTION, {
    timeout: 20_000,
  });
  return { ...created, row, path, launched };
}

/** The project view's word that it is loading a session's settings. */
const loadingSettings = (page: Page) =>
  page.getByRole('status').filter({ hasText: 'Loading settings…' });

/** Asks for the row's session to be restarted with settings. */
async function restartWithSettings(page: Page, row: Locator) {
  await row.getByTestId('session-title').click({ button: 'right' });
  await page
    .getByRole('menu', { name: /^Actions for session / })
    .getByRole('menuitem', { name: 'Restart with settings…' })
    .click();
}

test('the container group summarizes its changes and sends them nested', async ({
  page,
  request,
}) => {
  const { card, projectId, workstreamId } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);

  const header = form.getByRole('button', { name: /^Container / });
  const summary = form.getByTestId('group-summary');
  await expect(header).toHaveAttribute('aria-expanded', 'false');
  await expect(summary).toHaveText('Defaults');
  await expect(
    form.getByRole('checkbox', { name: 'playwright-mcp', exact: true }),
  ).toBeHidden();

  await header.click();
  await expect(header).toHaveAttribute('aria-expanded', 'true');
  await form
    .getByRole('checkbox', { name: 'playwright-mcp', exact: true })
    .check();
  const domains = form.getByRole('textbox', {
    name: 'Add to Extra allowed domains',
  });
  await domains.fill('api.example.com');
  await domains.press('Enter');
  await expect(form.getByText('api.example.com')).toBeVisible();

  await form.getByRole('button', { name: 'Add directory' }).click();
  const row = form.getByRole('group', { name: 'Directory 1' });
  await expect(row.getByRole('textbox', { name: 'Path' })).toBeFocused();
  // Left blank it is not sent, so it is no change.
  await expect(summary).toHaveText(
    'Features +playwright-mcp · Extra allowed domains +1',
  );
  await row.getByRole('textbox', { name: 'Path' }).fill(tmpdir());
  await row.getByRole('checkbox', { name: 'Read-only' }).uncheck();

  await expect(summary).toHaveText(
    'Features +playwright-mcp · Directories +1 · Extra allowed domains +1',
  );

  // What Enclave applies anyway is shown, and cannot be removed here.
  await expect(form.getByText('3000', { exact: true })).toBeVisible();
  await expect(form.getByRole('button', { name: 'Delete 3000' })).toHaveCount(0);
  await expect(form.getByText('From Enclave project config')).toBeVisible();
  const inherited = form.getByRole('group', {
    name: 'Directory from Enclave global config',
  });
  await expect(inherited.getByRole('textbox', { name: 'Path' })).toHaveValue(
    '/home/demo/shared',
  );
  await expect(inherited.getByRole('textbox', { name: 'Path' })).toBeDisabled();
  await expect(inherited.getByRole('button')).toHaveCount(0);

  await header.click();
  await expect(header).toHaveAttribute('aria-expanded', 'false');
  await expect(summary).toHaveText(
    'Features +playwright-mcp · Directories +1 · Extra allowed domains +1',
  );

  await form.getByRole('button', { name: 'Start session' }).click();
  await expect(form).toHaveCount(0);

  // What changed in the group, and nothing left at its default, so a default
  // that later changes is not pinned. A changed checklist is its whole set.
  const { launched } = await launchConfigOf(request, projectId, workstreamId);
  // The option's schema it also carries is the fake's, not what was sent.
  expect({ ...launched, schema: undefined }).toEqual({
    providerId: 'fake',
    optionId: 'sandboxed',
    config: {
      model: 'sonnet',
      enclave: {
        features: ['devtools', 'node-dev', 'playwright-mcp'],
        dirs: [{ path: tmpdir(), readonly: false }],
        allowDomains: ['api.example.com'],
      },
    },
  });
});

test('a session started on the defaults records an empty group', async ({
  page,
  request,
}) => {
  const { card, projectId, workstreamId } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);
  await form.getByRole('button', { name: 'Start session' }).click();
  await expect(form).toHaveCount(0);

  const { launched } = await launchConfigOf(request, projectId, workstreamId);
  expect(launched.config).toEqual({ model: 'sonnet', enclave: {} });
});

test('a list of values is one box: a click on its edge puts the caret in it', async ({
  page,
  request,
}) => {
  const { card } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);
  await form.getByRole('button', { name: /^Container / }).click();

  const domains = form.getByRole('textbox', {
    name: 'Add to Extra allowed domains',
  });
  await domains.fill('api.example.com');
  await domains.press('Enter');
  await domains.blur();
  // A point the box itself takes the click at — its padding, a gap between
  // chips — rather than the text box or a chip, found as the browser lays
  // them out.
  const point = await domains.locator('..').evaluate((box) => {
    const rect = box.getBoundingClientRect();
    for (let y = rect.top + 1; y < rect.bottom - 1; y += 1) {
      for (let x = rect.left + 1; x < rect.right - 1; x += 1) {
        if (box.ownerDocument.elementFromPoint(x, y) === box) return { x, y };
      }
    }
    return null;
  });
  if (!point) throw new Error('the list box shows none of itself');
  await page.mouse.click(point.x, point.y);
  await expect(domains).toBeFocused();
});

test('what is typed in a list but not added is not dropped by Start session', async ({
  page,
  request,
}) => {
  const { card, projectId, workstreamId } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);
  await form.getByRole('button', { name: /^Container / }).click();
  const ports = form.getByRole('textbox', {
    name: 'Add to Reach host services',
  });
  await ports.fill('5432x');

  // One click: leaving the box changes nothing under the pointer, so the
  // click lands, and the submit refuses what it cannot add.
  await form.getByRole('button', { name: 'Start session' }).click();
  await expect(form).toBeVisible();
  await expect(ports).toBeFocused();
  await expect(form.getByText('Enter a whole number.')).toBeVisible();
  await expect(form.getByText(/^Not added/)).toBeVisible();
  expect(await sessionsOf(request, projectId, workstreamId)).toEqual([]);

  // Corrected, the submit adds it, and sends it.
  await ports.fill('5432');
  await form.getByRole('button', { name: 'Start session' }).click();
  await expect(form).toHaveCount(0);
  const { launched } = await launchConfigOf(request, projectId, workstreamId);
  expect(launched.config).toEqual({
    model: 'sonnet',
    enclave: { bridgePorts: [5432] },
  });
});

test('one click on Start session sends what is typed in the lists', async ({
  page,
  request,
}) => {
  const { card, projectId, workstreamId } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);
  await form.getByRole('button', { name: /^Container / }).click();
  const domains = form.getByRole('textbox', {
    name: 'Add to Extra allowed domains',
  });
  for (const domain of ['a.example', 'b.example', 'c.example']) {
    await domains.fill(domain);
    await domains.press('Enter');
  }
  await form
    .getByRole('textbox', { name: 'Add to Reach host services' })
    .fill('5432');
  // Added as a chip, it takes a line of its own, moving what is below it.
  await domains.fill('a-much-longer-domain-name.example.com');

  await form.getByRole('button', { name: 'Start session' }).click();
  await expect(form).toHaveCount(0);
  const { launched } = await launchConfigOf(request, projectId, workstreamId);
  expect(launched.config).toEqual({
    model: 'sonnet',
    enclave: {
      bridgePorts: [5432],
      allowDomains: [
        'a.example',
        'b.example',
        'c.example',
        'a-much-longer-domain-name.example.com',
      ],
    },
  });
});

test('a value removed by keyboard hands its focus to the next one', async ({
  page,
  request,
}) => {
  const { card } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);
  await form.getByRole('button', { name: /^Container / }).click();
  const domains = form.getByRole('textbox', {
    name: 'Add to Extra allowed domains',
  });
  for (const domain of ['a.example', 'b.example']) {
    await domains.fill(domain);
    await domains.press('Enter');
  }

  await form.getByRole('button', { name: 'Delete a.example' }).focus();
  await page.keyboard.press('Enter');
  await expect(
    form.getByRole('button', { name: 'Delete b.example' }),
  ).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(domains).toBeFocused();
});

test('a field is described by what it does', async ({ page, request }) => {
  const { card } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);
  await form.getByRole('button', { name: /^Container / }).click();
  await expect(
    form.getByRole('checkbox', { name: 'Pass through my host agent config' }),
  ).toHaveAccessibleDescription(
    "Use the agent's configuration from your machine instead of the container's own.",
  );
  await expect(
    form.getByRole('group', { name: 'Reach host services' }),
  ).toHaveAccessibleDescription(
    'Host ports the container can reach on localhost, e.g. a database running on your machine.',
  );
});

test('a directory removed by keyboard hands its focus to the next one', async ({
  page,
  request,
}) => {
  const { card } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);
  await form.getByRole('button', { name: /^Container / }).click();
  for (const path of ['/srv/a', '/srv/b']) {
    await form.getByRole('button', { name: 'Add directory' }).click();
    await page.keyboard.type(path);
  }

  await form.getByRole('button', { name: 'Delete directory 1' }).focus();
  await page.keyboard.press('Enter');
  await expect(
    form.getByRole('button', { name: 'Delete directory 1' }),
  ).toBeFocused();
  await expect(
    form
      .getByRole('group', { name: 'Directory 1' })
      .getByRole('textbox', { name: 'Path' }),
  ).toHaveValue('/srv/b');
  await expect(form.getByRole('group', { name: 'Directory 2' })).toHaveCount(0);
});

test('a right-click in the form opens no workstream menu', async ({
  page,
  request,
}) => {
  const { card } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);
  await form.getByRole('button', { name: /^Container / }).click();

  await form.getByText('Features', { exact: true }).click({ button: 'right' });
  await form.getByRole('checkbox', { name: 'devtools' }).click({ button: 'right' });
  await expect(page.getByRole('menu')).toHaveCount(0);
});

test('a refused field shows its reason inside the group, opened for it', async ({
  page,
  request,
}) => {
  const { card } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);
  const header = form.getByRole('button', { name: /^Container / });
  await header.click();
  const ports = form.getByRole('textbox', { name: 'Add to Reach host services' });
  await ports.fill('70000');
  await ports.press('Enter');
  // Collapsed again: the refusal has to open it.
  await header.click();
  await expect(header).toHaveAttribute('aria-expanded', 'false');

  await form.getByRole('button', { name: 'Start session' }).click();

  await expect(header).toHaveAttribute('aria-expanded', 'true');
  const field = form.getByRole('group', { name: 'Reach host services' });
  // The daemon's wording; the limit is what any wording of it names.
  await expect(field.getByText(/65,?535/)).toBeVisible();
  await expect(ports).toBeFocused();
  await expect(form).toBeVisible();
});

test('a refused row alone is marked and focused, past the inherited ones', async ({
  page,
  request,
}) => {
  const { card } = await emptyCard(page, request);
  const form = await openSandboxedForm(page, card);
  await form.getByRole('button', { name: /^Container / }).click();
  await form.getByRole('button', { name: 'Add directory' }).click();
  await page.keyboard.type(tmpdir());
  await form.getByRole('button', { name: 'Add directory' }).click();
  await page.keyboard.type('/srv/missing');
  await form.getByRole('button', { name: 'Start session' }).focus();
  // As the Enclave provider refuses a directory that is not there.
  await page.route('**/sessions', (route) =>
    route.request().method() === 'POST'
      ? route.fulfill({
          status: 400,
          contentType: 'application/json',
          body: JSON.stringify({
            error: 'directory does not exist',
            fields: { '/enclave/dirs/1/path': 'directory does not exist' },
          }),
        })
      : route.continue(),
  );

  await page.keyboard.press('Enter');

  const path = (row: number) =>
    form
      .getByRole('group', { name: `Directory ${row}` })
      .getByRole('textbox', { name: 'Path' });
  await expect(path(2)).toBeFocused();
  await expect(path(2)).toHaveAttribute('aria-invalid', 'true');
  await expect(path(2)).toHaveAccessibleDescription('Directory does not exist');
  await expect(path(1)).not.toHaveAttribute('aria-invalid');
});

test('a launched value Enclave now applies anyway shows once, locked', async ({
  page,
  request,
}) => {
  const { row, path } = await sandboxedSession(page, request, {
    enclave: { ports: ['3000', '8080'] },
  });
  await restartWithSettings(page, row);
  const dialog = page.getByRole('dialog', { name: `Restart ${OPTION}` });
  await expect(dialog.getByText('8080', { exact: true })).toBeVisible();
  await expect(dialog.getByText('3000', { exact: true })).toHaveCount(1);
  await expect(
    dialog.getByRole('button', { name: 'Delete 3000' }),
  ).toHaveCount(0);

  await dialog.getByRole('button', { name: 'Restart', exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect
    .poll(
      async () =>
        (await (await request.get(`${path}/launch-config`)).json()).config,
      { timeout: 20_000 },
    )
    .toEqual({ model: 'sonnet', enclave: { ports: ['8080'] } });
});

test('Restart with settings opens on the launch config and restarts with the change', async ({
  page,
  request,
}) => {
  const { card, projectId, workstreamId } = await emptyCard(page, request);
  const created = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    {
      data: {
        providerId: 'fake',
        optionId: 'sandboxed',
        config: { model: 'opus', enclave: { allowDomains: ['a.example'] } },
      },
    },
  );
  expect(created.ok()).toBe(true);
  const { path } = await launchConfigOf(request, projectId, workstreamId);
  const row = card.getByRole('listitem').first();
  await expect(row.getByTestId('session-title')).toHaveText(OPTION, {
    timeout: 20_000,
  });

  await row.getByTestId('session-title').click({ button: 'right' });
  await page
    .getByRole('menu', { name: /^Actions for session / })
    .getByRole('menuitem', { name: 'Restart with settings…' })
    .click();

  const dialog = page.getByRole('dialog', { name: `Restart ${OPTION}` });
  await expect(dialog.getByRole('radio', { name: 'opus' })).toHaveAttribute(
    'aria-checked',
    'true',
  );
  await expect(
    dialog.getByRole('button', { name: /^Container / }),
  ).toHaveAttribute('aria-expanded', 'true');
  await expect(dialog.getByText('a.example', { exact: true })).toBeVisible();
  const fresh = dialog.getByRole('checkbox', {
    name: 'Start a fresh conversation',
  });
  await expect(fresh).not.toBeChecked();
  await expect(fresh).toHaveAccessibleDescription(
    "Otherwise the agent continues its latest conversation in this session's directory",
  );

  await dialog.getByRole('radio', { name: 'sonnet' }).click();
  const domains = dialog.getByRole('textbox', {
    name: 'Add to Extra allowed domains',
  });
  await domains.fill('b.example');
  await domains.press('Enter');
  await dialog.getByRole('button', { name: 'Restart', exact: true }).click();
  await expect(dialog).toHaveCount(0);

  await expect
    .poll(
      async () => (await (await request.get(`${path}/launch-config`)).json()).config,
      { timeout: 20_000 },
    )
    .toEqual({
      model: 'sonnet',
      enclave: { allowDomains: ['a.example', 'b.example'] },
    });
});

test('a session sharing its directory with another of its type restarts fresh, and says why', async ({
  page,
  request,
}) => {
  const { card, projectId, workstreamId } = await emptyCard(page, request);
  const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`;
  for (const count of [1, 2]) {
    const res = await request.post(base, {
      data: { providerId: 'fake', optionId: 'sandboxed', config: {} },
    });
    expect(res.ok()).toBe(true);
    await pollFor(
      `${count} sessions`,
      async () =>
        (await sessionsOf(request, projectId, workstreamId)).filter(
          (session) => !session.id.startsWith('starting-'),
        ).length === count || undefined,
      20_000,
    );
  }
  const row = card.getByRole('listitem').first();
  await expect(row.getByTestId('session-title')).toHaveText(OPTION, {
    timeout: 20_000,
  });

  await restartWithSettings(page, row);
  const dialog = page.getByRole('dialog', { name: `Restart ${OPTION}` });
  const fresh = dialog.getByRole('checkbox', {
    name: 'Start a fresh conversation',
  });
  await expect(fresh).toBeChecked();
  await expect(fresh).toBeDisabled();
  await expect(fresh).toHaveAccessibleDescription(
    /^Cannot continue the session's conversation: another Sandboxed demo session works in /,
  );

  const restart = page.waitForResponse('**/restart');
  await dialog.getByRole('button', { name: 'Restart', exact: true }).click();
  const response = await restart;
  expect(response.request().postDataJSON()).toMatchObject({ fresh: true });
  expect(response.status()).toBe(202);
  await expect(dialog).toHaveCount(0);
});

test('a project switch drops settings still loading, so they load afresh', async ({
  page,
  request,
}) => {
  const other = await workstreamWithSessions(request, 'hs-ui-other', 'x', 0);
  const { row, projectPrefix } = await sandboxedSession(page, request);

  let release = () => undefined as void;
  const held = new Promise<void>((resolve) => (release = resolve));
  let first = true;
  await page.route('**/launch-config', async (route) => {
    if (first) {
      first = false;
      await held;
    }
    await route.continue();
  });
  await restartWithSettings(page, row);
  await expect(loadingSettings(page)).toBeVisible();
  await selectProject(page, other.projectPrefix);
  await expect(
    page.getByRole('region', { name: `Workstream ${other.workstreamName}` }),
  ).toBeVisible();

  // Back before the first load answers: it no longer holds the dialog up.
  await selectProject(page, projectPrefix);
  await restartWithSettings(page, row);
  const dialog = page.getByRole('dialog', { name: `Restart ${OPTION}` });
  await expect(dialog).toBeVisible();
  await dialog.getByRole('radio', { name: 'opus' }).click();

  // The first load's answer leaves the open dialog, and what was changed in
  // it, alone.
  const answered = page.waitForResponse('**/launch-config');
  release();
  await answered;
  await expect(page.getByRole('dialog')).toHaveCount(1);
  await expect(dialog.getByRole('radio', { name: 'opus' })).toHaveAttribute(
    'aria-checked',
    'true',
  );
  await expect(loadingSettings(page)).toHaveCount(0);
});

test('Restart with settings says it is loading them until they are there', async ({
  page,
  request,
}) => {
  const { row } = await sandboxedSession(page, request);
  let release = () => undefined as void;
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route('**/launch-config', async (route) => {
    await held;
    await route.continue();
  });
  await restartWithSettings(page, row);
  await expect(loadingSettings(page)).toBeVisible();
  release();
  await expect(
    page.getByRole('dialog', { name: `Restart ${OPTION}` }),
  ).toBeVisible();
  await expect(loadingSettings(page)).toHaveCount(0);
});

test('Restart with settings says why the settings could not be loaded', async ({
  page,
  request,
}) => {
  const { row } = await sandboxedSession(page, request);
  await page.route('**/launch-config', (route) =>
    route.fulfill({
      status: 409,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'the session type is no longer offered' }),
    }),
  );
  await restartWithSettings(page, row);
  await expect(
    page.getByRole('alert').filter({
      hasText: /^The session type is no longer offered$/,
    }),
  ).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);

  await page.unroute('**/launch-config');
  // As the daemon answers when the provider cannot build the option's schema.
  await page.route('**/launch-config', (route) =>
    route.fulfill({
      status: 503,
      contentType: 'application/json',
      body: JSON.stringify({
        error:
          "provider is unavailable: could not read Enclave's settings for the session; try again (exit status 1)",
      }),
    }),
  );
  await restartWithSettings(page, row);
  await expect(
    page.getByRole('alert').filter({
      hasText:
        /^Provider is unavailable: could not read Enclave's settings for the session; try again/,
    }),
  ).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);

  await page.unroute('**/launch-config');
  await page.route('**/launch-config', (route) => route.abort());
  await restartWithSettings(page, row);
  await expect(
    page
      .getByRole('alert')
      .filter({ hasText: /^Could not reach the daemon\.$/ }),
  ).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('a restart the provider cannot read the settings for keeps the dialog open', async ({
  page,
  request,
}) => {
  const { row } = await sandboxedSession(page, request);
  await page.route('**/restart', (route) =>
    route.fulfill({
      status: 503,
      contentType: 'application/json',
      body: JSON.stringify({
        error:
          "provider is unavailable: could not read Enclave's settings for the session; try again (exit status 1)",
      }),
    }),
  );
  await restartWithSettings(page, row);
  const dialog = page.getByRole('dialog', { name: `Restart ${OPTION}` });
  await dialog.getByRole('button', { name: 'Restart', exact: true }).click();
  await expect(dialog.getByRole('alert')).toHaveText(
    /^Provider is unavailable: could not read Enclave's settings for the session; try again/,
  );
  await expect(dialog).toBeVisible();
});

test('a restart with no settings to show keeps what the session was started with', async ({
  page,
  request,
}) => {
  const { row } = await sandboxedSession(page, request, {
    model: 'opus',
    enclave: { allowDomains: ['a.example'] },
  });
  // As for an option whose schema has no fields.
  await page.route('**/launch-config', async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      json: { ...(await response.json()), schema: {} },
    });
  });
  await restartWithSettings(page, row);
  const dialog = page.getByRole('dialog', { name: `Restart ${OPTION}` });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('radio')).toHaveCount(0);
  const restart = page.waitForResponse('**/restart');
  // Restart is pressed by its letter, as Start session is.
  await page.keyboard.press('Alt+s');
  // No config, so the daemon replays the recorded one rather than defaults.
  const response = await restart;
  expect(response.request().postDataJSON()).toEqual({ fresh: false });
  expect(response.status()).toBe(202);
  await expect(dialog).toHaveCount(0);
});

test('a recorded feature no longer offered is listed checked, to uncheck', async ({
  page,
  request,
}) => {
  const { row, path } = await sandboxedSession(page, request, {
    enclave: { features: ['devtools'] },
  });
  // As if the provider had dropped a feature since the session started.
  await page.route('**/launch-config', async (route) => {
    const response = await route.fetch();
    const launched = await response.json();
    launched.config.enclave.features.push('retired');
    await route.fulfill({ response, json: launched });
  });
  await restartWithSettings(page, row);
  const dialog = page.getByRole('dialog', { name: `Restart ${OPTION}` });
  const retired = dialog.getByRole('checkbox', { name: 'retired' });
  await expect(retired).toBeChecked();
  await expect(retired).toHaveAccessibleDescription('Not offered any more');
  await expect(dialog.getByTestId('group-summary')).toContainText('+retired');

  await retired.uncheck();
  await dialog.getByRole('button', { name: 'Restart', exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await page.unroute('**/launch-config');
  await expect
    .poll(
      async () =>
        (await (await request.get(`${path}/launch-config`)).json()).config,
      { timeout: 20_000 },
    )
    .toEqual({ model: 'sonnet', enclave: { features: ['devtools'] } });
});

test('Escape leaves the restart dialog without restarting', async ({
  page,
  request,
}) => {
  const { card, projectId, workstreamId } = await emptyCard(page, request);
  await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'sandboxed', config: {} } },
  );
  const { path, launched } = await launchConfigOf(
    request,
    projectId,
    workstreamId,
  );
  const row = card.getByRole('listitem').first();
  await expect(row.getByTestId('session-title')).toHaveText(OPTION, {
    timeout: 20_000,
  });
  await row.focus();
  await page.keyboard.press('Shift+F10');
  await page
    .getByRole('menu', { name: /^Actions for session / })
    .getByRole('menuitem', { name: 'Restart with settings…' })
    .click();
  const dialog = page.getByRole('dialog', { name: `Restart ${OPTION}` });
  await expect(dialog.getByRole('radio', { name: 'sonnet' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(row).toBeFocused();
  expect(await (await request.get(`${path}/launch-config`)).json()).toEqual(
    launched,
  );
});

// A dropdown shows the hand while it can be used, and not while the form is
// inert, the same as its hover border.
test("a dropdown's cursor follows whether it can be used", async ({
  page,
  request,
}) => {
  const { card } = await emptyCard(page, request);
  await card.getByRole('button', { name: 'New session' }).click();
  const form = page.getByRole('form', { name: 'New session' });
  // OpenCode's model enum has no default, so it is a <select>, not pills.
  await form.getByRole('radio', { name: 'OpenCode' }).click();
  const select = form.locator('select').first();
  await expect(select).toHaveCSS('cursor', 'pointer');

  // The start never answers, so the form stays inert while it is out.
  await page.route('**/api/projects/*/workstreams/*/sessions', async (route) => {
    if (route.request().method() !== 'POST') await route.continue();
  });
  await form.getByRole('button', { name: 'Start session' }).click();
  await expect(select).toHaveAttribute('aria-disabled', 'true');
  await expect(select).toHaveCSS('cursor', 'not-allowed');
});
