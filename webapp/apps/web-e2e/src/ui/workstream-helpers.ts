import { basename } from 'node:path';
import {
  type APIRequestContext,
  type Locator,
  type Page,
} from '@playwright/test';
import { expect, tempProjectDir } from '../fixtures';
import {
  createProject,
  createSession,
  createWorkstream,
  sessionsOf,
} from '../api/workstream-helpers';
import { addEmptyProject, addProjectWithDefault } from '../projects';
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

/**
 * Adds a fresh project without its default workstream, loads the app and
 * selects it; returns its name.
 */
export async function selectEmptyProject(
  page: Page,
  request: APIRequestContext,
  prefix: string,
) {
  const dir = await tempProjectDir(prefix);
  await addEmptyProject(request, dir);
  await loadApp(page);
  await selectProject(page, basename(dir));
  return basename(dir);
}

/**
 * Adds a project over `dir` with its default workstream, loads the app and
 * selects it; returns the project's ID and that workstream.
 */
export async function selectProjectWithDefault(
  page: Page,
  request: APIRequestContext,
  dir: string,
) {
  const added = await addProjectWithDefault(request, dir);
  await loadApp(page);
  await selectProject(page, basename(dir));
  return added;
}

/** Opens the selected project's new-workstream form; returns it, on the name. */
export async function openNewWorkstreamForm(page: Page) {
  await page.getByRole('button', { name: 'New workstream' }).click();
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
 * A workstream whose files really are on the daemon host: the default one a
 * project comes with, in place, so its working directory is the project
 * directory itself, renamed to a name of its own. The fake provider records a
 * working directory it never creates, which is not enough for the actions that
 * need real files.
 */
export async function materializedWorkstream(
  request: APIRequestContext,
  prefix: string,
  namePrefix: string,
): Promise<MaterializedWorkstream> {
  const dir = await tempProjectDir(prefix);
  const { projectId, workstream } = await addProjectWithDefault(request, dir);
  const workstreamName = unique(namePrefix);
  const renamed = await request.patch(
    `/api/projects/${projectId}/workstreams/${workstream.id}`,
    { data: { name: workstreamName } },
  );
  expect(renamed.status()).toBe(204);
  return {
    dirName: basename(dir),
    workstreamName,
    projectId,
    workstreamId: workstream.id,
  };
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
 * A `workstreamWithSessions` workstream with real sessions in it: `ended`
 * stopped ones, then `running` running ones; returns their API paths in that
 * order. Made one at a time: the fake runs at most three sessions at once, and
 * an ended one does not count.
 */
export async function workstreamWithEndedSessions(
  request: APIRequestContext,
  prefix: string,
  namePrefix: string,
  { ended, running }: { ended: number; running: number },
) {
  const created = await workstreamWithSessions(request, prefix, namePrefix, 0);
  const base = `/api/projects/${created.projectId}/workstreams/${created.workstreamId}/sessions`;
  const paths: string[] = [];
  for (let i = 0; i < ended + running; i++) {
    const session = await createSession(
      request,
      created.projectId,
      created.workstreamId,
    );
    const path = `${base}/${session.id}`;
    paths.push(path);
    if (i < ended) {
      expect((await request.post(`${path}/stop`)).status()).toBe(204);
    } else {
      await runSession(request, path);
    }
  }
  return { ...created, paths };
}

/**
 * Waits until `card` is the real workstream's, not the placeholder that stands
 * for it while it is created (that one is `aria-busy`).
 */
export async function waitForRealCard(card: Locator) {
  await expect(card).toBeVisible({ timeout: 20_000 });
  await expect(card).not.toHaveAttribute('aria-busy', 'true', {
    timeout: 20_000,
  });
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
  return { ...created, card, row: sessionRows(card).first() };
}

/**
 * A card's session rows: its list items with actions of their own, not the
 * "Ended" divider or the fold.
 */
export function sessionRows(card: Locator) {
  return card.getByRole('listitem').filter({
    has: card.page().getByRole('button', { name: 'More actions' }),
  });
}

/** Opens the More actions menu of the session `row`. */
export async function openMoreActions(row: Locator) {
  await row.getByRole('button', { name: 'More actions' }).click();
}

/** Picks Delete in the More actions menu of the session `row`. */
export async function deleteSession(row: Locator) {
  await openMoreActions(row);
  await row.page().getByRole('menuitem', { name: 'Delete' }).click();
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
