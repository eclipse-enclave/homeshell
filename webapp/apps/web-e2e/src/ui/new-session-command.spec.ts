import { expect, test } from '../fixtures';
import { controlEventStream } from './event-stream';
import { loadApp, searchCommands } from './keyboard-helpers';
import { createWorkstream } from '../api/workstream-helpers';
import {
  selectProject,
  unique,
  waitForRealCard,
  workstreamWithSessions,
} from './workstream-helpers';

/**
 * The palette's New session starts one only where the card's own New session
 * would: not in a workstream the daemon is still setting up.
 */

test('New session skips a workstream that is still being set up', async ({
  page,
  request,
}) => {
  const { projectPrefix, workstreamName, projectId } =
    await workstreamWithSessions(request, 'hs-ui-new-session', 'ready', 0);
  const stream = await controlEventStream(page);
  await loadApp(page);
  await selectProject(page, projectPrefix);
  await waitForRealCard(
    page.getByRole('region', { name: `Workstream ${workstreamName}` }),
  );

  const state = await (await request.get('/api/projects')).json();
  const project = state.projects.find(
    (candidate: { id: string }) => candidate.id === projectId,
  );
  project.workstreams.push({
    ...project.workstreams[0],
    id: 'creating-e2e',
    name: 'setting-up',
    state: 'creating',
    effectiveState: 'creating',
    sessions: [],
  });
  await stream.hold(state);
  const settingUp = page.getByRole('region', { name: 'Workstream setting-up' });
  await expect(settingUp).toBeVisible();
  await expect(
    settingUp.getByRole('button', { name: 'New session' }),
  ).toBeDisabled();

  const palette = await searchCommands(page, 'new session');
  await expect(palette.getByRole('option', { name: /New session/ })).toHaveText(
    [new RegExp(`^Workstream "${workstreamName}": New session`)],
  );
  await page.keyboard.press('Escape');

  await settingUp.focus();
  await searchCommands(page, 'new session');
  await expect(
    palette.getByRole('option', { name: /New session/ }),
  ).toHaveCount(0);
  await stream.release();
});

// Asking which workstream is modal: a shortcut must not stack the palette or
// the shortcut reference over it.
test('the workstream picker keeps the shortcuts out', async ({
  page,
  request,
}) => {
  const { projectPrefix, workstreamName, projectId } =
    await workstreamWithSessions(request, 'hs-ui-new-session', 'first', 0);
  await createWorkstream(request, projectId, unique('second'));
  await loadApp(page);
  await selectProject(page, projectPrefix);
  await expect(
    page.getByRole('region', { name: `Workstream ${workstreamName}` }),
  ).toBeVisible();

  await page.keyboard.press('Control+Shift+Backquote');
  const picker = page.getByRole('dialog', { name: 'New session in' });
  await expect(picker).toBeVisible();
  await page.keyboard.press('F1');
  await page.keyboard.press('?');

  await expect(page.getByRole('dialog')).toHaveCount(1);
  await expect(picker.getByRole('combobox')).toHaveValue('?');
});

// A key the palette lets through runs its command with the palette closed.
test('the New session key closes the palette to ask which workstream', async ({
  page,
  request,
}) => {
  const { projectPrefix, workstreamName, projectId } =
    await workstreamWithSessions(request, 'hs-ui-new-session', 'first', 0);
  await createWorkstream(request, projectId, unique('second'));
  await loadApp(page);
  await selectProject(page, projectPrefix);
  await expect(
    page.getByRole('region', { name: `Workstream ${workstreamName}` }),
  ).toBeVisible();

  await page.keyboard.press('F1');
  await expect(
    page.getByRole('dialog', { name: 'Command palette' }),
  ).toBeVisible();
  await page.keyboard.press('Control+Shift+Backquote');

  await expect(
    page.getByRole('dialog', { name: 'New session in' }),
  ).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(1);
});
