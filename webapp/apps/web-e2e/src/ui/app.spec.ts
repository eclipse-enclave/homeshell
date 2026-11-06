import { expect, tempProjectDir, test } from '../fixtures';
import { isPlaceholderSession } from '../placeholders';
import { createWorkstreamInForm, selectProject } from './workstream-helpers';

// The daemon seed. release-tools is the demo simulator's target, so its state
// cycles and is not pinned here.
const PROJECTS = [
  { name: 'homeshell', state: 'Waiting for input' },
  { name: 'acme-webshop', state: 'Running' },
  { name: 'release-tools', state: null },
  { name: 'infra-tools', state: 'Error' },
  { name: 'notes', state: 'Idle' },
];

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('renders the shell and reports the daemon as connected', async ({
  page,
}) => {
  await expect(page.getByText('HomeShell', { exact: true })).toBeVisible();
  await expect(
    page.getByRole('img', { name: 'Daemon connected' }),
  ).toBeVisible();
});

test('loads project state over the SSE stream (token auth via the vite proxy)', async ({
  page,
}) => {
  // Vite injects the API token as a Bearer header on proxied /api requests, so
  // a seed project rendering proves the SSE snapshot arrived under token auth.
  const nav = page.getByRole('navigation', { name: 'Projects' });
  await expect(nav.getByRole('button', { name: /homeshell/ })).toBeVisible();
});

test('shows the daemon version and offers no update when up to date', async ({
  page,
}) => {
  // The e2e daemon is a source build without a release channel, so no update shows.
  await expect(page.getByText(/^daemon /)).toBeVisible();
  await expect(page.getByRole('button', { name: /update to/i })).toHaveCount(0);
});

test('a rolling build shows its channel beside the commit and offers the update', async ({
  page,
}) => {
  // Only a channel-stamped release build reports this; the e2e daemon is not one.
  await page.route('**/api/version', (route) =>
    route.fulfill({
      json: {
        channel: 'rolling',
        current: 'abc1234',
        latest: 'def5678',
        updateAvailable: true,
      },
    }),
  );
  await page.reload();
  await expect(page.getByText('daemon abc1234 (rolling)')).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Update to def5678' }),
  ).toBeVisible();
});

test('lists every project with its daemon-derived state', async ({ page }) => {
  const nav = page.getByRole('navigation', { name: 'Projects' });
  await expect(nav.getByRole('button').first()).toBeVisible();

  for (const project of PROJECTS) {
    const item = nav.getByRole('button', { name: new RegExp(project.name) });
    await expect(item).toBeVisible();
    if (project.state) {
      await expect(
        item.getByRole('img', { name: project.state }),
      ).toBeVisible();
    }
  }
});

test('shows the first project with its workstreams and sessions by default', async ({
  page,
}) => {
  await expect(
    page.getByRole('heading', { level: 1, name: 'homeshell' }),
  ).toBeVisible();

  const workstream = page.getByRole('region', {
    name: 'Workstream feature/self-update',
  });
  await expect(workstream).toBeVisible();
  await expect(
    workstream.getByText('Fake Provider', { exact: true }),
  ).toBeVisible();
  await expect(workstream.getByText('Implement self-update')).toBeVisible();
  await expect(
    workstream.getByText('Verify update flow end-to-end'),
  ).toBeVisible();
  // One session runs, one waits: the card counts them by state beside the
  // name, the one waiting on the user first, rather than one aggregate label.
  await expect(workstream.getByTestId('workstream-sessions')).toHaveAttribute(
    'aria-label',
    'Sessions: 1 waiting for input, 1 running',
  );
  // A live workstream's own state is what its sessions say, so it is not
  // repeated.
  await expect(workstream.getByTestId('workstream-state')).toHaveCount(0);
});

test('switching the project swaps the workstream panel', async ({ page }) => {
  const nav = page.getByRole('navigation', { name: 'Projects' });

  await nav.getByRole('button', { name: /acme-webshop/ }).click();
  await expect(
    page.getByRole('heading', { level: 1, name: 'acme-webshop' }),
  ).toBeVisible();
  await expect(
    page.getByRole('heading', { name: 'bugfix/checkout-tax' }),
  ).toBeVisible();

  await nav.getByRole('button', { name: /notes/ }).click();
  await expect(page.getByText('No workstreams yet')).toBeVisible();
});

test('streams live demo updates without a reload', async ({ page }) => {
  const nav = page.getByRole('navigation', { name: 'Projects' });
  await nav.getByRole('button', { name: /release-tools/ }).click();

  const workstream = page.getByRole('region', {
    name: 'Workstream release-notes-1.4',
  });
  // The demo simulator creates a fresh session every few seconds and walks it
  // through running/done, which flips the workstream state.
  await expect(workstream.getByText(/Demo run #/).first()).toBeVisible({
    timeout: 15_000,
  });
  const badges = workstream.getByTestId('workstream-sessions');
  const initial = await badges.getAttribute('aria-label');
  await expect
    .poll(() => badges.getAttribute('aria-label'), { timeout: 15_000 })
    .not.toBe(initial);
});

test('a session title updates live from the daemon', async ({ page }) => {
  const nav = page.getByRole('navigation', { name: 'Projects' });
  await nav.getByRole('button', { name: /release-tools/ }).click();

  const workstream = page.getByRole('region', {
    name: 'Workstream release-notes-1.4',
  });
  // The demo cycler renames a still-running session mid-run.
  await expect(
    workstream.getByText(/Demo run #\d+ · editing files/).first(),
  ).toBeVisible({
    timeout: 15_000,
  });
});

test('a finished demo session reads as "Done" (UI-computed overlay)', async ({
  page,
}) => {
  const nav = page.getByRole('navigation', { name: 'Projects' });
  await nav.getByRole('button', { name: /release-tools/ }).click();

  const workstream = page.getByRole('region', {
    name: 'Workstream release-notes-1.4',
  });
  // The daemon reports the finished session as `idle`; the web app computes
  // `done = idle && !seen`. The generous timeout allows two full demo cycles.
  await expect(
    workstream.getByTestId('session-state').filter({ hasText: 'Done' }).first(),
  ).toBeVisible({ timeout: 30_000 });
});

test('a stopped workstream shows "Stopped", not its session state', async ({
  page,
}) => {
  const nav = page.getByRole('navigation', { name: 'Projects' });
  await nav.getByRole('button', { name: /release-tools/ }).click();

  const workstream = page.getByRole('region', {
    name: 'Workstream release-notes-1.3',
  });
  await expect(workstream).toBeVisible();
  // The stopped runtime overrides the session aggregate ("Done").
  await expect(
    workstream.getByTestId('workstream-state').getByText('Stopped'),
  ).toBeVisible();
});

test('an errored workstream shows "Error", not its session state', async ({
  page,
}) => {
  const nav = page.getByRole('navigation', { name: 'Projects' });
  await nav.getByRole('button', { name: /infra-tools/ }).click();

  const workstream = page.getByRole('region', {
    name: 'Workstream deploy-pipeline',
  });
  await expect(workstream).toBeVisible();
  // The errored runtime takes precedence over "Running".
  await expect(
    workstream.getByTestId('workstream-state').getByText('Error'),
  ).toBeVisible();
});

test('the New workstream and New session actions are functional', async ({
  page,
}) => {
  await expect(
    page.getByRole('button', { name: 'New workstream' }).first(),
  ).toBeEnabled();
  // The fake provider supports session creation.
  await expect(
    page.getByRole('button', { name: 'New session' }).first(),
  ).toBeEnabled();
});

test('new workstream: creates one via the fake provider', async ({ page }) => {
  const { basename } = await import('node:path');
  const dir = await tempProjectDir('hs-ui-ws-');
  const dirName = basename(dir);

  // A throwaway project, so the seed data is left alone.
  await page.getByRole('button', { name: 'Add project' }).click();
  await page.getByPlaceholder('/path/to/project').fill(dir);
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(page.getByPlaceholder('/path/to/project')).not.toBeVisible();

  const nav = page.getByRole('navigation', { name: 'Projects' });
  await nav.getByRole('button', { name: new RegExp(dirName) }).click();
  await expect(page.getByText('No workstreams yet')).toBeVisible();

  await page.getByRole('button', { name: 'New workstream' }).first().click();
  // Pick the fake provider explicitly: Enclave, where it is installed, also
  // offers to create here and its schema collides on "Name".
  await page.getByRole('radio', { name: 'Fake Provider' }).click();
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill('e2e-ws');
  await page.getByRole('textbox', { name: 'Branch' }).fill('feature/e2e');
  await createWorkstreamInForm(page, () =>
    page.getByRole('button', { name: 'Create' }).click(),
  );

  await expect(
    page.getByRole('region', { name: 'Workstream e2e-ws' }),
  ).toBeVisible();
});

test('new session: create one through the UI form', async ({
  page,
  request,
}) => {
  const { basename } = await import('node:path');
  const dir = await tempProjectDir('hs-ui-sess-');
  const dirName = basename(dir);

  // A project and empty workstream via the API, then drive the UI.
  const projectId = (
    await (await request.post('/api/projects', { data: { path: dir } })).json()
  ).id;
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'fake',
      name: 'ui-sessions',
      config: { branch: 'main' },
    },
  });

  await page.goto('/');
  await selectProject(page, dirName);
  const workstream = page.getByRole('region', {
    name: 'Workstream ui-sessions',
  });
  await expect(workstream).toBeVisible();

  await workstream.getByRole('button', { name: 'New session' }).click();
  // The fake provider offers two agents, so an agent selector shows.
  const form = page.getByRole('form', { name: 'New session' });
  await expect(form.getByLabel('Agent')).toBeVisible();
  await form.getByRole('button', { name: 'Start session' }).click();

  await expect(workstream.getByText('Claude Code').first()).toBeVisible();
});

test('a session state change reaches a second window without a reload', async ({
  browser,
  request,
}) => {
  const { basename } = await import('node:path');
  const dir = await tempProjectDir('hs-ui-live-');
  const dirName = basename(dir);

  const projectId = (
    await (await request.post('/api/projects', { data: { path: dir } })).json()
  ).id;
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'live-ws', config: { branch: 'main' } },
  });

  // Creation is asynchronous: wait for the placeholder to be replaced.
  let wsId = '';
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const project = state.projects?.find(
      (p: { id: string }) => p.id === projectId,
    );
    const real = project?.workstreams?.find(
      (w: { id: string }) => !String(w.id).startsWith('creating-'),
    );
    if (real) {
      wsId = real.id;
      break;
    }
    await new Promise((r) => setTimeout(r, 50));
  }
  expect(wsId).toBeTruthy();

  await request.post(
    `/api/projects/${projectId}/workstreams/${wsId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude' } },
  );

  let sess = { id: '' };
  const sessDeadline = Date.now() + 5000;
  while (Date.now() < sessDeadline) {
    const state = await (await request.get('/api/projects')).json();
    const ws = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find((w: { id: string }) => w.id === wsId);
    const real = ws?.sessions?.find(
      (s: { id: string }) => !isPlaceholderSession(s.id),
    );
    if (real) {
      sess = real;
      break;
    }
    await new Promise((r) => setTimeout(r, 50));
  }
  expect(sess.id).toBeTruthy();

  const contexts = [await browser.newContext(), await browser.newContext()];
  const pages = await Promise.all(contexts.map((c) => c.newPage()));
  for (const p of pages) {
    await p.goto('/');
    await p
      .getByRole('navigation', { name: 'Projects' })
      .getByRole('button', { name: new RegExp(dirName) })
      .click();
    await expect(
      p.getByRole('region', { name: 'Workstream live-ws' }),
    ).toBeVisible();
  }

  // Change the session state from outside both windows.
  await request.post(
    `/api/projects/${projectId}/workstreams/${wsId}/sessions/${sess.id}/stop`,
  );

  // Both windows reflect it live, without a reload.
  for (const p of pages) {
    await expect(
      p
        .getByRole('region', { name: 'Workstream live-ws' })
        .getByTestId('session-state')
        .getByText('Stopped'),
    ).toBeVisible();
  }
  await Promise.all(contexts.map((c) => c.close()));
});

test('add project: opens form, cancels, and re-opens', async ({ page }) => {
  const addButton = page.getByRole('button', { name: 'Add project' });
  await expect(addButton).toBeEnabled();
  await addButton.click();
  await expect(page.getByPlaceholder('/path/to/project')).toBeVisible();
  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.getByPlaceholder('/path/to/project')).not.toBeVisible();
});

test('add project: creates a project from a real directory', async ({
  page,
}) => {
  const { basename } = await import('node:path');
  const dir = await tempProjectDir('hs-ui-e2e-');
  const dirName = basename(dir);

  await page.getByRole('button', { name: 'Add project' }).click();
  await page.getByPlaceholder('/path/to/project').fill(dir);
  await page.getByRole('button', { name: 'Add', exact: true }).click();

  await expect(page.getByPlaceholder('/path/to/project')).not.toBeVisible();
  const nav = page.getByRole('navigation', { name: 'Projects' });
  await expect(
    nav.getByRole('button', { name: new RegExp(dirName) }),
  ).toBeVisible();
});

test('add project: shows an error for a bad path', async ({ page }) => {
  await page.getByRole('button', { name: 'Add project' }).click();
  await page
    .getByPlaceholder('/path/to/project')
    .fill('/no/such/directory/zzz');
  await page.getByRole('button', { name: 'Add', exact: true }).click();

  // Scoped to the form: a notification toast is a `role="alert"` too, so an
  // unscoped locator resolves to two elements the moment one is on screen.
  await expect(
    page.getByRole('form', { name: 'Add project' }).getByRole('alert'),
  ).toBeVisible();
  // The form stays open so the user can correct the input.
  await expect(page.getByPlaceholder('/path/to/project')).toBeVisible();
});

/**
 * Stubs the native folder chooser for a test.
 *
 * Every Browse click asks the daemon for the *host desktop's* chooser first, and
 * the daemon obliges: left alone, these tests would pop a real GTK/macOS folder
 * dialog on whoever runs `make test-e2e` and then wait forever for someone to
 * answer it (the same reason the host terminal and desktop-IDE open modes have
 * no e2e). Stubbing the endpoint is what makes each outcome
 * — a chosen path, a dismissal, no chooser at all — reachable and deterministic
 * without a window ever opening.
 */
// Drives /api/browse/native without a real dialog. The e2e daemon runs with
// HOMESHELL_NO_NATIVE_DIALOG set (playwright.config.ts), so an unstubbed Browse
// answers 501 and falls back to the in-app picker rather than hanging the run on
// a window nobody can see; these stubs are what make the *other* outcomes
// reachable.
async function stubNativeFolderChooser(
  page: import('@playwright/test').Page,
  outcome:
    | { status: 501 }
    | { status: 200; path: string }
    | { status: 200; cancelled: true },
) {
  const { status, ...body } = outcome;
  await page.route('**/api/browse/native', (route) =>
    route.fulfill({
      status,
      contentType: 'application/json',
      body: JSON.stringify(
        status === 501 ? { error: 'no native folder chooser on this host' } : body,
      ),
    }),
  );
}

test('add project: Browse fills the path from the host desktop chooser', async ({
  page,
}) => {
  await stubNativeFolderChooser(page, {
    status: 200,
    path: '/tmp/chosen-by-the-desktop',
  });

  await page.getByRole('button', { name: 'Add project' }).click();
  await page.getByRole('button', { name: 'Browse' }).click();

  await expect(page.getByPlaceholder('/path/to/project')).toHaveValue(
    '/tmp/chosen-by-the-desktop',
  );
  // The desktop answered, so the in-app picker must never appear.
  await expect(
    page.getByRole('dialog', { name: 'Select folder' }),
  ).toHaveCount(0);

  await page.getByRole('button', { name: 'Cancel' }).click();
});

test('add project: dismissing the host desktop chooser leaves the form alone', async ({
  page,
}) => {
  await stubNativeFolderChooser(page, { status: 200, cancelled: true });

  await page.getByRole('button', { name: 'Add project' }).click();
  await page.getByRole('button', { name: 'Browse' }).click();

  // Back to an idle Browse button, an empty path, and no in-app picker: a
  // dismissal means "not now", not "show me the other picker instead".
  await expect(page.getByRole('button', { name: 'Browse' })).toBeEnabled();
  await expect(page.getByPlaceholder('/path/to/project')).toHaveValue('');
  await expect(
    page.getByRole('dialog', { name: 'Select folder' }),
  ).toHaveCount(0);

  await page.getByRole('button', { name: 'Cancel' }).click();
});

test('add project: folder picker selects a directory into the path field', async ({
  page,
}) => {
  // No chooser on this host — the in-app picker is the fallback that keeps
  // browsing possible, and is also what a networked daemon always gets.
  await stubNativeFolderChooser(page, { status: 501 });

  await page.getByRole('button', { name: 'Add project' }).click();
  await page.getByRole('button', { name: 'Browse' }).click();

  const dialog = page.getByRole('dialog', { name: 'Select folder' });
  await expect(dialog).toBeVisible();

  await expect(dialog.getByRole('button', { name: 'Select' })).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeVisible();

  const currentPath = await dialog.getByTestId('current-path').textContent();
  await dialog.getByRole('button', { name: 'Select' }).click();

  await expect(dialog).not.toBeVisible();
  const pathInput = page.getByPlaceholder('/path/to/project');
  await expect(pathInput).toHaveValue(currentPath!);

  await page.getByRole('button', { name: 'Cancel' }).click();
});

test('add project: folder picker closes on Escape', async ({ page }) => {
  await stubNativeFolderChooser(page, { status: 501 });

  await page.getByRole('button', { name: 'Add project' }).click();
  await page.getByRole('button', { name: 'Browse' }).click();

  const dialog = page.getByRole('dialog', { name: 'Select folder' });
  await expect(dialog).toBeVisible();

  await page.keyboard.press('Escape');
  await expect(dialog).not.toBeVisible();

  await expect(page.getByPlaceholder('/path/to/project')).toBeVisible();
  await page.getByRole('button', { name: 'Cancel' }).click();
});

test('add project: the folder picker opens on the typed path', async ({
  page,
}) => {
  const { mkdtemp, realpath } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const { join } = await import('node:path');
  const dir = await realpath(await mkdtemp(join(tmpdir(), 'hs-ui-pick-')));
  await stubNativeFolderChooser(page, { status: 501 });

  await page.getByRole('button', { name: 'Add project' }).click();
  await page.getByRole('textbox', { name: 'Path', exact: true }).fill(dir);
  await page.getByRole('button', { name: 'Browse' }).click();

  const dialog = page.getByRole('dialog', { name: 'Select folder' });
  await expect(dialog.getByTestId('current-path')).toHaveText(dir);
  await page.keyboard.press('Escape');

  // A path the daemon cannot list opens at home instead of on an error.
  await page
    .getByRole('textbox', { name: 'Path', exact: true })
    .fill(join(dir, 'missing'));
  await page.getByRole('button', { name: 'Browse' }).click();
  await expect(dialog.getByTestId('current-path')).toBeVisible();
  await expect(dialog.getByTestId('current-path')).not.toHaveText(
    join(dir, 'missing'),
  );
  await expect(dialog.getByText('path does not exist')).toHaveCount(0);
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: 'Cancel' }).click();
});

test('rename project: inline edit updates the name', async ({ page }) => {
  const { basename } = await import('node:path');
  const dir = await tempProjectDir('hs-ui-ren-');
  const dirName = basename(dir);

  // A throwaway project, so the seed data is left alone.
  await page.getByRole('button', { name: 'Add project' }).click();
  await page.getByPlaceholder('/path/to/project').fill(dir);
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(page.getByPlaceholder('/path/to/project')).not.toBeVisible();

  const nav = page.getByRole('navigation', { name: 'Projects' });
  await nav.getByRole('button', { name: new RegExp(dirName) }).click();
  await expect(
    page.getByRole('heading', { level: 1, name: dirName }),
  ).toBeVisible();

  // No Rename button: the name itself renames the project.
  await page.getByRole('heading', { level: 1, name: dirName }).dblclick();
  const input = page.getByLabel('Project name');
  await expect(input).toBeVisible();
  await input.fill('renamed-project');
  await input.press('Enter');

  await expect(
    page.getByRole('heading', { level: 1, name: 'renamed-project' }),
  ).toBeVisible();
});

test('rename workstream: inline edit updates the card name', async ({
  page,
  request,
}) => {
  const { mkdtemp } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const { basename, join } = await import('node:path');
  const dir = await mkdtemp(join(tmpdir(), 'hs-ui-wsren-'));
  const dirName = basename(dir);

  const projectId = (
    await (await request.post('/api/projects', { data: { path: dir } })).json()
  ).id;
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'before-rename', config: { branch: 'main' } },
  });

  await page.goto('/');
  await selectProject(page, dirName);

  const workstream = page.getByRole('region', {
    name: 'Workstream before-rename',
  });
  await expect(workstream).toBeVisible();

  await workstream.getByRole('heading', { name: 'before-rename' }).dblclick();
  const input = workstream.getByLabel('Workstream name');
  await expect(input).toBeVisible();
  await input.fill('after-rename');
  await input.press('Enter');

  await expect(
    page.getByRole('region', { name: 'Workstream after-rename' }),
  ).toBeVisible();
});

test('remove project: removes the project from the list', async ({ page }) => {
  // A throwaway project, so the seed data is left alone.
  const { basename } = await import('node:path');
  const dir = await tempProjectDir('hs-ui-rm-');
  const dirName = basename(dir);

  await page.getByRole('button', { name: 'Add project' }).click();
  await page.getByPlaceholder('/path/to/project').fill(dir);
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(page.getByPlaceholder('/path/to/project')).not.toBeVisible();

  const nav = page.getByRole('navigation', { name: 'Projects' });
  const item = nav.getByRole('button', { name: new RegExp(dirName) });
  await expect(item).toBeVisible();

  await item.click();
  await expect(
    page.getByRole('heading', { level: 1, name: dirName }),
  ).toBeVisible();
  await page.getByRole('button', { name: `Delete project ${dirName}` }).click();
  await page
    .getByRole('dialog', { name: `Delete "${dirName}"?` })
    .getByRole('button', { name: 'Delete' })
    .click();

  await expect(item).not.toBeVisible();
});

test('the toolbar theme toggle switches the theme', async ({ page }) => {
  const html = page.locator('html');
  const before = await html.getAttribute('data-theme');
  await page
    .getByRole('button', { name: /Switch to (light|dark) theme/ })
    .click();
  await expect.poll(() => html.getAttribute('data-theme')).not.toBe(before);
});

test('a workstream offers the one open choice the daemon declares', async ({
  page,
  request,
}) => {
  const { basename } = await import('node:path');
  const dir = await tempProjectDir('hs-ui-open-');
  const dirName = basename(dir);

  // A throwaway project and fake workstream, so the seed data is left alone.
  const projectId = (
    await (await request.post('/api/projects', { data: { path: dir } })).json()
  ).id;
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'open-ws', config: { branch: 'main' } },
  });

  await page.goto('/');
  await selectProject(page, dirName);
  const workstream = page.getByRole('region', { name: 'Workstream open-ws' });
  await expect(workstream).toBeVisible();

  // No materialized directory and no session, so the daemon declares no modes.
  // Exact: the caret half's name ("… with…") contains the primary half's.
  const openAction = workstream.getByRole('button', {
    name: 'Open workstream open-ws',
    exact: true,
  });
  await expect(openAction).toHaveCount(0);

  // A session gives the workstream an environment, so modes appear.
  const workstreams = await (await request.get('/api/projects')).json();
  const wsId = workstreams.projects
    .find((p: { id: string }) => p.id === projectId)
    .workstreams.find((w: { id: string }) => !w.id.startsWith('creating-')).id;
  await request.post(
    `/api/projects/${projectId}/workstreams/${wsId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude' } },
  );
  await expect(openAction).toBeVisible();

  // Open leads the header action row, and Delete ends it, as in the menu.
  const labels = await workstream
    .getByRole('button')
    .evaluateAll((nodes) =>
      nodes.map(
        (node) =>
          node.getAttribute('aria-label') ?? node.textContent?.trim() ?? '',
      ),
    );
  const header = labels.slice(
    0,
    labels.indexOf('Delete workstream open-ws') + 1,
  );
  expect(header[0]).toBe('Open workstream open-ws');
  // Then creating, as on every item; no Rename button among them.
  expect(header.indexOf('New session')).toBeGreaterThan(0);
  expect(header).not.toContain('Rename');

  // The host Terminal needs the folder, which the fake never creates, so no
  // mode is the plain open's default: the control is the menu alone, without a
  // default half that could only fail.
  //
  // Wait for the menu: while the session is still starting it offers its link
  // but no shell yet, and a single mode without a default is a plain button
  // that would open that link straight away.
  await expect(openAction).toHaveAttribute('aria-haspopup', 'menu');
  await expect(
    workstream.getByRole('button', { name: 'Open workstream open-ws with…' }),
  ).toHaveCount(0);
  await openAction.click();
  const menu = page.getByRole('menu', { name: 'Open workstream open-ws' });
  const shell = menu.getByRole('menuitem', { name: /^Shell in / });
  await expect(shell).toBeVisible();
  // Nothing clicks the link item: resolving it opens a browser tab.
  await expect(menu.getByRole('menuitem', { name: 'Web IDE' })).toBeVisible();
  await expect(menu.getByRole('menuitem', { name: 'Terminal' })).toHaveCount(0);

  // Safe to click: the fake provider stubs the launch, so nothing spawns.
  const opened = page.waitForResponse((res) =>
    res.url().endsWith(`/workstreams/${wsId}/open-with`),
  );
  await shell.click();
  expect((await opened).status()).toBe(200);
  await expect(page.getByRole('menu')).toHaveCount(0);
  await expect(page.getByRole('main').getByRole('alert')).toHaveCount(0);
});
