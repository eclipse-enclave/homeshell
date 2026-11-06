import { basename } from 'node:path';
import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { selectProject } from './workstream-helpers';

/**
 * A session whose creation failed, on the card: the row keeps its place, badges
 * itself `Error`, and prints the provider's reason underneath. The × beside that
 * reason removes the session outright — it is not a way to hide the text, which
 * would leave a row that still says "error" and can no longer say why.
 */

async function projectWithWorkstream(
  request: APIRequestContext,
): Promise<{ dirName: string; projectId: string; workstreamId: string }> {
  const dir = await tempProjectDir('hs-ui-session-error-');
  const created = await request.post('/api/projects', { data: { path: dir } });
  expect(created.status()).toBe(201);
  const projectId = (await created.json()).id as string;

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'fake',
      name: 'ui-session-error',
      config: { branch: 'main' },
    },
  });
  expect(res.status()).toBe(201);

  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const ws = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find((w: { id: string }) => !w.id.startsWith('creating-'));
    if (ws) return { dirName: basename(dir), projectId, workstreamId: ws.id };
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error('timed out waiting for the workstream');
}

/** An unknown optionId is accepted (202) and fails in the provider. */
async function startFailingSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
) {
  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'no-such-agent', config: {} } },
  );
  expect(res.status()).toBe(202);
}

async function openCard(page: Page, dirName: string) {
  await page.goto('/');
  await selectProject(page, dirName);
  const card = page.getByRole('region', { name: 'Workstream ui-session-error' });
  await expect(card).toBeVisible();
  return card;
}

test('a failed session says why, on its own row', async ({ page, request }) => {
  const { dirName, projectId, workstreamId } =
    await projectWithWorkstream(request);
  const card = await openCard(page, dirName);

  await startFailingSession(request, projectId, workstreamId);

  // The row that was "Starting" becomes the error, in place.
  await expect(card.getByTestId('session-state')).toHaveText('Error', {
    timeout: 15_000,
  });
  await expect(card.getByTestId('session-error')).toContainText(
    'no-such-agent',
  );

  // The failure is the session's, so the workstream keeps no copy of the text.
  await expect(
    card.getByRole('button', { name: /Dismiss message for workstream/ }),
  ).toHaveCount(0);
});

test('the × on the error removes the session completely', async ({
  page,
  request,
}) => {
  const { dirName, projectId, workstreamId } =
    await projectWithWorkstream(request);
  const card = await openCard(page, dirName);

  await startFailingSession(request, projectId, workstreamId);
  const remove = card.getByRole('button', { name: /Delete failed session/ });
  await expect(remove).toBeVisible({ timeout: 15_000 });

  // No confirmation: there is no runtime behind the row to lose.
  await remove.click();

  // The whole row goes, not just the message — and on the daemon's own state
  // push, which is what makes it survive a reload.
  await expect(card.getByTestId('session-error')).toHaveCount(0, {
    timeout: 10_000,
  });
  await expect(card.getByTestId('session-state')).toHaveCount(0);
  await expect(card.getByTestId('workstream-sessions')).toHaveAttribute(
    'aria-label',
    'Sessions: No sessions',
  );

  await page.reload();
  const reloaded = page.getByRole('region', {
    name: 'Workstream ui-session-error',
  });
  await expect(reloaded).toBeVisible();
  await expect(reloaded.getByTestId('session-error')).toHaveCount(0);
});

test('a failed session offers no restart with settings', async ({
  page,
  request,
}) => {
  const { dirName, projectId, workstreamId } =
    await projectWithWorkstream(request);
  const card = await openCard(page, dirName);
  await startFailingSession(request, projectId, workstreamId);
  await expect(card.getByTestId('session-state')).toHaveText('Error', {
    timeout: 15_000,
  });

  await card.getByTestId('session-title').click({ button: 'right' });
  const menu = page.getByRole('menu', { name: /^Actions for session / });
  await expect(menu.getByRole('menuitem', { name: 'Delete' })).toBeVisible();
  await expect(
    menu.getByRole('menuitem', { name: 'Restart with settings…' }),
  ).toHaveCount(0);
});
