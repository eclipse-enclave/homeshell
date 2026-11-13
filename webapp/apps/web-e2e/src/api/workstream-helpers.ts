import { expect, type APIRequestContext } from '@playwright/test';

import { tempProjectDir } from '../fixtures';
import { isPlaceholderSession } from '../placeholders';
import { addEmptyProject } from '../projects';

/**
 * Shared setup for the api specs that need a project with a workstream, and
 * the reads and polls of the projection they check it through. This is a
 * helper module, not a spec: the api project's testMatch (`api/*.spec.ts`)
 * skips it.
 */

/**
 * Creates a fresh project over a throwaway temp dir and returns its ID.
 *
 * The directory comes from `tempProjectDir`, which registers it with the
 * `throwawayProjects` fixture, so the project and the directory are removed
 * after the test rather than piling up in a daemon that outlives the run. A spec
 * only gets that if it takes `test` from `../fixtures`; one importing from
 * `@playwright/test` still creates the project, it just never gets swept.
 */
export async function createProject(
  request: APIRequestContext,
  prefix = 'hs-e2e-',
): Promise<string> {
  return addEmptyProject(request, await tempProjectDir(prefix));
}

export interface RealWorkstream {
  id: string;
  name: string;
  strategyId?: string;
  strategy?: string;
  provider: string;
  providerLabel?: string;
  branch: string;
  workingDir?: string;
  inProjectFolder?: boolean;
  openModes: {
    id: string;
    label: string;
    opensInBrowser?: boolean;
    default?: boolean;
  }[];
  /** The workstream-level actions the daemon offers right now (e.g. the note). */
  actions?: { id: string; label: string; description?: string }[];
  sessions?: ListedSession[];
}

/**
 * Polls GET /api/projects until a non-placeholder workstream (id not starting
 * with "creating-") appears in the project. Workstream creation is asynchronous
 * for every provider, so the POST returns a placeholder and the real workstream
 * lands shortly after.
 */
export async function awaitRealWorkstream(
  request: APIRequestContext,
  projectId: string,
  timeoutMs = 5000,
): Promise<RealWorkstream> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const project = state.projects?.find(
      (p: { id: string }) => p.id === projectId,
    );
    const real = project?.workstreams?.find(
      (w: { id: string }) => !String(w.id).startsWith('creating-'),
    );
    if (real) return real as RealWorkstream;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`timed out waiting for real workstream in ${projectId}`);
}

/**
 * Creates a workstream via the fake provider, waits for the asynchronous
 * creation to finish, and returns the real workstream's ID. Found by name, as
 * the project may hold others (its default one, say).
 */
export async function createWorkstream(
  request: APIRequestContext,
  projectId: string,
  name = 'e2e',
): Promise<string> {
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name, config: { branch: 'main' } },
  });
  expect(res.status()).toBe(201);
  return (await awaitWorkstreamNamed(request, projectId, name)).id;
}

/**
 * Polls until the project lists a real (no longer creating) workstream under
 * `name`, and returns it. Creation swaps the placeholder for a workstream with
 * another ID, so it is found by name.
 */
export async function awaitWorkstreamNamed(
  request: APIRequestContext,
  projectId: string,
  name: string,
): Promise<RealWorkstream> {
  return pollFor(`workstream ${name}`, async () => {
    const state = await (await request.get('/api/projects')).json();
    return state.projects
      ?.find((project: { id: string }) => project.id === projectId)
      ?.workstreams?.find(
        (workstream: { id: string; name: string }) =>
          workstream.name === name && !workstream.id.startsWith('creating-'),
      ) as RealWorkstream | undefined;
  });
}

/**
 * Starts a fake-provider Claude session in the workstream and returns it once
 * it is a real session rather than its placeholder.
 */
export async function createSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
): Promise<ListedSession> {
  const before = new Set(
    (await sessionsOf(request, projectId, workstreamId)).map(
      (session) => session.id,
    ),
  );
  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );
  expect(res.status()).toBe(202);
  return pollFor('the new session', async () =>
    (await sessionsOf(request, projectId, workstreamId)).find(
      (session) => !isPlaceholderSession(session.id) && !before.has(session.id),
    ),
  );
}

/** The core's create-form strategy property, as the project's form serves it. */
interface StrategyProperty {
  enum: string[];
  enumNames: string[];
  default: string;
}

/** The strategy property of the core's create form for the project. */
export async function coreStrategy(
  request: APIRequestContext,
  projectId: string,
): Promise<StrategyProperty> {
  const res = await request.get(
    `/api/projects/${projectId}/workstream-providers`,
  );
  expect(res.status()).toBe(200);
  const providers = await res.json();
  const core = providers.find(
    (descriptor: { id: string }) => descriptor.id === 'core',
  );
  return core.workstreamSchema.properties.strategy;
}

/** Polls until `pick` returns something, then returns it. */
export async function pollFor<T>(
  what: string,
  pick: () => Promise<T | undefined>,
  timeoutMs = 10_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const picked = await pick();
    if (picked !== undefined) return picked;
    await new Promise((resolve) => setTimeout(resolve, 40));
  }
  throw new Error(`timed out waiting for ${what}`);
}

/** The workstream as GET /api/projects lists it right now, if it is listed. */
export async function workstreamOf(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
): Promise<RealWorkstream | undefined> {
  const state = await (await request.get('/api/projects')).json();
  return state.projects
    ?.find((project: { id: string }) => project.id === projectId)
    ?.workstreams?.find(
      (workstream: { id: string }) => workstream.id === workstreamId,
    );
}

export interface ListedSession {
  observationData?: {
    matchedRule?: string;
    confident?: boolean;
    visibleBlocker?: boolean;
    gone?: boolean;
    readError?: string;
  };
  id: string;
  agent: string;
  state: string;
  preview?: string;
  error?: string;
  provider?: string;
  placeholderId?: string;
  restartable?: boolean;
  outdatedSettings?: string[];
  /** Whether the session has ended: stopped or done, not a failed start. */
  ended: boolean;
  /** When the daemon saw the session end; absent while it is live. */
  endedAt?: string;
}

/** The workstream's sessions as GET /api/projects lists them right now. */
export async function sessionsOf(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
): Promise<ListedSession[]> {
  return (await workstreamOf(request, projectId, workstreamId))?.sessions ?? [];
}
