import { basename } from 'node:path';
import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import {
  isPlaceholderSession,
  isPlaceholderWorkstream,
} from '../placeholders';
import { selectProject, unique } from './workstream-helpers';

/**
 * The UI half of the removal guard: the daemon's 409 becomes a confirmation that
 * explains sessions are running, and confirming re-issues the removal with
 * force. Set-up goes through the API (a running session is not something the UI
 * can arrange), then the flow is driven entirely through the page.
 */

/**
 * Polls until `read` yields an id, each call getting its own budget. Shared
 * deadlines are what make a slow first wait look like a broken second one.
 */
async function pollForId(
  what: string,
  read: () => Promise<string | undefined>,
  timeoutMs = 15000,
): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const found = await read();
    if (found) return found;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`timed out waiting for ${what}`);
}

/** Creates a project holding one workstream with one running session. */
async function projectWithRunningSession(
  request: APIRequestContext,
): Promise<{ dirName: string; workstreamName: string }> {
  const dir = await tempProjectDir('hs-ui-wsrm-');
  const created = await request.post('/api/projects', { data: { path: dir } });
  expect(created.status()).toBe(201);
  const projectId = (await created.json()).id as string;

  // A name no parallel worker collides with: the daemon is shared, and both
  // tests here would otherwise open the same card.
  const workstreamName = unique('ui-removal');
  const createdWorkstream = await request.post(
    `/api/projects/${projectId}/workstreams`,
    {
      data: {
        providerId: 'fake',
        name: workstreamName,
        config: { branch: 'main' },
      },
    },
  );
  expect(createdWorkstream.status()).toBe(201);

  const workstreamsOf = async () => {
    const state = await (await request.get('/api/projects')).json();
    return state.projects?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams;
  };
  const workstreamId = await pollForId(
    'the workstream',
    async () =>
      (await workstreamsOf())?.find(
        (w: { id: string }) => !isPlaceholderWorkstream(w.id),
      )?.id,
  );

  const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`;
  const createdSession = await request.post(base, {
    data: { providerId: 'fake', optionId: 'claude' },
  });
  expect(createdSession.status()).toBe(202);
  const sessionId = await pollForId(
    'the session',
    async () =>
      (await workstreamsOf())
        ?.find((w: { id: string }) => w.id === workstreamId)
        ?.sessions?.find((s: { id: string }) => !isPlaceholderSession(s.id))
        ?.id,
  );

  // The fake creates sessions idle; stop→start makes one running.
  expect((await request.post(`${base}/${sessionId}/stop`)).status()).toBe(204);
  expect((await request.post(`${base}/${sessionId}/start`)).status()).toBe(204);

  return { dirName: basename(dir), workstreamName };
}

/** Opens the project and returns its workstream card. */
async function openWorkstream(page: Page, dirName: string, name: string) {
  await page.goto('/');
  await selectProject(page, dirName);
  const card = page.getByRole('region', { name: `Workstream ${name}` });
  await expect(card).toBeVisible();
  return card;
}

test('removing a workstream with a running session confirms, then forces', async ({
  page,
  request,
}) => {
  const { dirName, workstreamName } = await projectWithRunningSession(request);
  const card = await openWorkstream(page, dirName, workstreamName);
  await expect(card.getByTestId('workstream-sessions')).toHaveAttribute(
    'aria-label',
    /^Sessions: 1 [a-z ]+$/,
  );

  await card
    .getByRole('button', { name: `Delete workstream ${workstreamName}` })
    .click();

  // First the ordinary delete dialog (files), which sends the request…
  const first = page.getByRole('dialog');
  await expect(first).toBeVisible();
  await first.getByRole('button', { name: 'Delete', exact: true }).click();

  // …and the daemon refuses it, which the UI turns into a second confirmation
  // that says why. This one comes from the rejection, not from a setting.
  const busy = page.getByRole('dialog');
  await expect(busy).toBeVisible();
  await expect(busy).toContainText('still busy');
  await expect(busy).toContainText('Claude Code');

  // The workstream is still there while the question stands.
  await expect(
    page.getByRole('region', { name: `Workstream ${workstreamName}` }),
  ).toBeVisible();

  await busy.getByRole('button', { name: 'Stop and delete' }).click();
  await expect(
    page.getByRole('region', { name: `Workstream ${workstreamName}` }),
  ).toHaveCount(0);
});

test('cancelling the running-session confirmation keeps the workstream', async ({
  page,
  request,
}) => {
  const { dirName, workstreamName } = await projectWithRunningSession(request);
  const card = await openWorkstream(page, dirName, workstreamName);

  await card
    .getByRole('button', { name: `Delete workstream ${workstreamName}` })
    .click();
  await page
    .getByRole('dialog')
    .getByRole('button', { name: 'Delete', exact: true })
    .click();

  const busy = page.getByRole('dialog');
  await expect(busy).toContainText('still busy');
  await busy.getByRole('button', { name: 'Cancel' }).click();

  await expect(busy).toHaveCount(0);
  await expect(
    page.getByRole('region', { name: `Workstream ${workstreamName}` }),
  ).toBeVisible();
  // And the session it was protecting is still running.
  await expect(
    card.getByTestId('session-state').filter({ hasText: 'Running' }),
  ).toBeVisible();
});
