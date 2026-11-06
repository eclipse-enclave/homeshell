import { expect, test, type APIRequestContext } from '@playwright/test';
import { createProject, createWorkstream } from './workstream-helpers';

async function workstreamName(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
): Promise<string | undefined> {
  const state = await (await request.get('/api/projects')).json();
  const ws = state.projects
    .find((p: { id: string }) => p.id === projectId)
    ?.workstreams.find((w: { id: string }) => w.id === workstreamId);
  return ws?.name as string | undefined;
}

/**
 * Black-box tests for workstream rename against the daemon's public API,
 * via the in-memory fake provider. UI-free, so they survive a daemon rewrite.
 */
test.describe('workstream rename API', () => {
  test('PATCH renames a workstream', async ({ request }) => {
    const projectId = await createProject(request, 'hs-ws-e2e-');
    const wsId = await createWorkstream(request, projectId, 'original');

    const res = await request.patch(
      `/api/projects/${projectId}/workstreams/${wsId}`,
      { data: { name: 'renamed' } },
    );
    expect(res.status()).toBe(204);

    await expect
      .poll(() => workstreamName(request, projectId, wsId))
      .toBe('renamed');
  });

  test('rejects an empty name with 400', async ({ request }) => {
    const projectId = await createProject(request, 'hs-ws-e2e-');
    const wsId = await createWorkstream(request, projectId, 'original');
    const res = await request.patch(
      `/api/projects/${projectId}/workstreams/${wsId}`,
      { data: { name: '   ' } },
    );
    expect(res.status()).toBe(400);
  });
});
