import { expect, type APIRequestContext } from '@playwright/test';

/**
 * Adding projects for the specs. The daemon gives a new project a default
 * workstream (in place, in the project folder); most specs are about the
 * workstreams they create themselves, and start from a project without it.
 */

export interface DefaultWorkstream {
  id: string;
  name: string;
  branch?: string;
  strategyId: string;
  workingDir: string;
  provider: string;
  inProjectFolder?: boolean;
}

/** Adds a project over `path` as the daemon sets it up, default workstream included. */
export async function addProjectWithDefault(
  request: APIRequestContext,
  path: string,
): Promise<{ projectId: string; workstream: DefaultWorkstream }> {
  const res = await request.post('/api/projects', { data: { path } });
  expect(res.status()).toBe(201);
  const project = await res.json();
  // Sessions already running in the project's folders are found at once, so
  // the default may not be the only workstream.
  const workstream = (project.workstreams as DefaultWorkstream[]).find(
    (candidate) =>
      candidate.strategyId === 'in-place' &&
      candidate.workingDir === project.path,
  );
  if (!workstream) {
    throw new Error(
      `project ${project.id} came without its default workstream`,
    );
  }
  return { projectId: project.id as string, workstream };
}

/** Adds a project over `path` and removes its default workstream; returns its ID. */
export async function addEmptyProject(
  request: APIRequestContext,
  path: string,
): Promise<string> {
  const { projectId, workstream } = await addProjectWithDefault(request, path);
  const removed = await request.delete(
    `/api/projects/${projectId}/workstreams/${workstream.id}`,
  );
  expect(removed.status()).toBe(204);
  return projectId;
}
