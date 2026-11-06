import { basename } from 'node:path';
import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, tempProjectDir } from '../fixtures';
import {
  createProject,
  createWorkstream,
  sessionsOf,
} from '../api/workstream-helpers';
import { settingsSaved, withSettingsLock } from '../settings-lock';
import { loadApp } from './keyboard-helpers';

/**
 * Shared setup for the ui specs: projects and workstreams made through the
 * API, and the steps that bring them up in the page. This is a helper module,
 * not a spec: the ui project's testMatch (`ui/*.spec.ts`) skips it — the same
 * treatment `src/api/workstream-helpers.ts` gets.
 */

/** Selects the project whose name matches `name` in the sidebar. */
export async function selectProject(page: Page, name: string) {
  await page
    .getByRole('navigation', { name: 'Projects' })
    .getByRole('button', { name: new RegExp(name) })
    .click();
}

/** Adds a fresh, empty project, loads the app and selects it; returns its name. */
export async function selectEmptyProject(
  page: Page,
  request: APIRequestContext,
  prefix: string,
) {
  const dir = await tempProjectDir(prefix);
  expect(
    (await request.post('/api/projects', { data: { path: dir } })).status(),
  ).toBe(201);
  await loadApp(page);
  await selectProject(page, basename(dir));
  return basename(dir);
}

/** Opens the selected project's new-workstream form; returns it, on the name. */
export async function openNewWorkstreamForm(page: Page) {
  await page.getByRole('button', { name: 'New workstream' }).first().click();
  const form = page.getByRole('form', { name: 'New workstream' });
  await expect(form.getByLabel('Name')).toBeFocused({ timeout: 20_000 });
  return form;
}

/**
 * Submits the new-workstream form by `submit`. A workstream created in the UI
 * records its provider as the last used one, which PUTs the whole settings
 * document, so the submit holds the settings lock until that write has landed.
 */
export async function createWorkstreamInForm(
  page: Page,
  submit: () => Promise<void>,
) {
  await withSettingsLock(async () => {
    const saved = settingsSaved(page);
    await submit();
    await saved;
  });
}

/** A name no parallel worker will collide with; the daemon is shared. */
export const unique = (name: string) =>
  `${name}-${Math.random().toString(36).slice(2, 8)}`;

export interface MaterializedWorkstream {
  /** Basename of the throwaway project directory, to pick the project in the nav. */
  dirName: string;
  workstreamName: string;
  projectId: string;
  workstreamId: string;
}

/**
 * A workstream whose files really are on the daemon host: an in-place core one,
 * so its working directory is the project directory itself. The fake provider
 * records a working directory it never creates, which is not enough for the
 * actions that need real files.
 */
export async function materializedWorkstream(
  request: APIRequestContext,
  prefix: string,
  namePrefix: string,
): Promise<MaterializedWorkstream> {
  const dir = await tempProjectDir(prefix);
  const project = await request.post('/api/projects', { data: { path: dir } });
  expect(project.status()).toBe(201);
  const projectId = (await project.json()).id as string;

  const workstreamName = unique(namePrefix);
  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      name: workstreamName,
      config: { strategy: 'in-place' },
    },
  });
  expect(created.status()).toBe(201);

  // Creation is asynchronous: wait for the placeholder to be replaced.
  const deadline = Date.now() + 8000;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const ws = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find(
        (w: { id: string; name: string }) =>
          w.name === workstreamName && !w.id.startsWith('creating-'),
      );
    if (ws) {
      return {
        dirName: basename(dir),
        workstreamName,
        projectId,
        workstreamId: ws.id as string,
      };
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`timed out waiting for workstream ${workstreamName}`);
}

/**
 * A fake-provider workstream with `sessions` sessions in it (still starting
 * when this returns: the e2e fake delays startup), for the specs that drive
 * the session rows rather than files on disk. The project's name starts with
 * the returned `projectPrefix`, and no other project's does.
 */
export async function workstreamWithSessions(
  request: APIRequestContext,
  prefix: string,
  namePrefix: string,
  sessions = 1,
): Promise<{
  projectPrefix: string;
  workstreamName: string;
  projectId: string;
  workstreamId: string;
}> {
  const projectPrefix = `${unique(prefix)}-`;
  const projectId = await createProject(request, projectPrefix);
  const workstreamName = unique(namePrefix);
  const workstreamId = await createWorkstream(
    request,
    projectId,
    workstreamName,
  );
  for (let i = 0; i < sessions; i++) {
    const session = await request.post(
      `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
      { data: { providerId: 'fake', optionId: 'claude', config: {} } },
    );
    expect(session.ok()).toBe(true);
  }
  return { projectPrefix, workstreamName, projectId, workstreamId };
}

/**
 * Loads the app on a fresh `workstreamWithSessions` workstream's card, once its
 * sessions are the real ones rather than their placeholders.
 */
export async function openWorkstreamCard(
  page: Page,
  request: APIRequestContext,
  prefix: string,
  namePrefix: string,
  sessions = 1,
) {
  const created = await workstreamWithSessions(
    request,
    prefix,
    namePrefix,
    sessions,
  );
  await loadApp(page);
  await selectProject(page, created.projectPrefix);
  const card = page.getByRole('region', {
    name: `Workstream ${created.workstreamName}`,
  });
  if (sessions > 0) {
    await expect(card.getByTestId('session-title')).toHaveText(
      Array(sessions).fill('Claude Code'),
      { timeout: 15_000 },
    );
  }
  return { ...created, card, row: card.getByRole('listitem').first() };
}

/** The API path of the workstream's first session. */
export async function firstSessionPath(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
) {
  const [session] = await sessionsOf(request, projectId, workstreamId);
  return `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${session.id}`;
}

/**
 * Leaves the idle session at `sessionPath` running: the fake creates sessions
 * idle, and a stopped one it starts again runs.
 */
export async function runSession(
  request: APIRequestContext,
  sessionPath: string,
) {
  expect((await request.post(`${sessionPath}/stop`)).status()).toBe(204);
  expect((await request.post(`${sessionPath}/start`)).status()).toBe(204);
}
