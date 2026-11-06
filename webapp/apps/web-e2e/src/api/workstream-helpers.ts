import { expect, type APIRequestContext } from '@playwright/test';

import { tempProjectDir } from '../fixtures';

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
  return addProject(request, await tempProjectDir(prefix));
}

/** Adds a project over an existing directory and returns its ID. */
export async function addProject(
  request: APIRequestContext,
  path: string,
): Promise<string> {
  const res = await request.post('/api/projects', { data: { path } });
  expect(res.status()).toBe(201);
  return (await res.json()).id as string;
}

export interface RealWorkstream {
  id: string;
  name: string;
  provider: string;
  providerLabel?: string;
  branch: string;
  workingDir?: string;
  openModes: { id: string; label: string }[];
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
 * creation to finish, and returns the real workstream's ID.
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
  return (await awaitRealWorkstream(request, projectId)).id;
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
}

/** The workstream's sessions as GET /api/projects lists them right now. */
export async function sessionsOf(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
): Promise<ListedSession[]> {
  return (await workstreamOf(request, projectId, workstreamId))?.sessions ?? [];
}
