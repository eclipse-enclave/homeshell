import { expect, test } from '@playwright/test';
import { awaitRealWorkstream, createProject } from './workstream-helpers';

/**
 * Black-box tests for the provider surface: capability discovery, the
 * project-scoped provider pick-list, and the workstream lifecycle. UI-free so
 * they survive a daemon rewrite. The daemon runs in demo mode, so the in-memory
 * fake provider is registered.
 */
test.describe('provider API', () => {
  test('GET /api/providers serves the fake provider and its capabilities', async ({
    request,
  }) => {
    const res = await request.get('/api/providers');
    expect(res.status()).toBe(200);
    const providers = await res.json();
    expect(Array.isArray(providers)).toBe(true);

    const fake = providers.find((p: { id: string }) => p.id === 'fake');
    expect(fake).toBeTruthy();
    expect(fake.label).toBeTruthy();
    expect(fake.status.state).toBe('available');
    // The fake session provider implements session start/stop and one-off
    // prompting (it echoes a prompt/key into the target session's preview for
    // demo determinism).
    expect(fake.capabilities.sessionStartStop).toBe(true);
    expect(fake.capabilities.oneOffPrompt).toBe(true);
  });

  test('GET /api/projects/{id}/workstream-providers serves the pick-list', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-prov-e2e-');
    const res = await request.get(
      `/api/projects/${projectId}/workstream-providers`,
    );
    expect(res.status()).toBe(200);
    const providers = await res.json();

    const fake = providers.find((p: { id: string }) => p.id === 'fake');
    expect(fake).toBeTruthy();
    expect(fake.label).toBeTruthy();
    expect(fake.workstreamSchema).toBeTruthy();
  });

  test('unknown project has no workstream providers', async ({ request }) => {
    const res = await request.get(
      '/api/projects/does-not-exist/workstream-providers',
    );
    expect(res.status()).toBe(404);
  });

  test('workstream lifecycle: create, reflect in state, rename, delete', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-prov-e2e-');

    // Create — asynchronous for every provider: the POST returns a
    // creating-state placeholder; the real workstream lands shortly after.
    const created = await request.post(
      `/api/projects/${projectId}/workstreams`,
      {
        data: {
          providerId: 'fake',
          name: 'feature-x',
          config: { branch: 'main' },
        },
      },
    );
    expect(created.status()).toBe(201);
    const ws = await awaitRealWorkstream(request, projectId);
    expect(ws.provider).toBe('fake');
    expect(ws.branch).toBe('main');

    // Rename.
    const renamed = await request.patch(
      `/api/projects/${projectId}/workstreams/${ws.id}`,
      {
        data: { name: 'renamed' },
      },
    );
    expect(renamed.status()).toBe(204);

    const afterRename = await (await request.get('/api/projects')).json();
    const updatedWs = afterRename.projects
      .find((p: { id: string }) => p.id === projectId)
      .workstreams.find((w: { id: string }) => w.id === ws.id);
    expect(updatedWs.name).toBe('renamed');

    // Delete.
    const deleted = await request.delete(
      `/api/projects/${projectId}/workstreams/${ws.id}`,
    );
    expect(deleted.status()).toBe(204);
    const afterDelete = await (await request.get('/api/projects')).json();
    const gone = afterDelete.projects.find(
      (p: { id: string }) => p.id === projectId,
    );
    expect(gone.workstreams.map((w: { id: string }) => w.id)).not.toContain(
      ws.id,
    );
  });

  test('delete with keepFiles option removes the workstream', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-prov-e2e-');
    const created = await request.post(
      `/api/projects/${projectId}/workstreams`,
      {
        data: {
          providerId: 'fake',
          name: 'keep-files-test',
          config: { branch: 'main' },
        },
      },
    );
    expect(created.status()).toBe(201);
    const ws = await awaitRealWorkstream(request, projectId);

    const deleted = await request.delete(
      `/api/projects/${projectId}/workstreams/${ws.id}`,
      { data: { keepFiles: true } },
    );
    expect(deleted.status()).toBe(204);

    const afterDelete = await (await request.get('/api/projects')).json();
    const gone = afterDelete.projects.find(
      (p: { id: string }) => p.id === projectId,
    );
    expect(gone.workstreams.map((w: { id: string }) => w.id)).not.toContain(
      ws.id,
    );
  });

  test('delete with empty body is backward compatible', async ({ request }) => {
    const projectId = await createProject(request, 'hs-prov-e2e-');
    const created = await request.post(
      `/api/projects/${projectId}/workstreams`,
      {
        data: {
          providerId: 'fake',
          name: 'compat-test',
          config: { branch: 'main' },
        },
      },
    );
    expect(created.status()).toBe(201);
    const ws = await awaitRealWorkstream(request, projectId);

    // DELETE with no body at all — backward compat.
    const deleted = await request.delete(
      `/api/projects/${projectId}/workstreams/${ws.id}`,
    );
    expect(deleted.status()).toBe(204);
  });

  test('opens a session and returns the open result', async ({ request }) => {
    // The demo seed includes a workstream with sessions; open the first one.
    const state = await (await request.get('/api/projects')).json();
    let ref: { p: string; w: string; s: string } | undefined;
    for (const p of state.projects) {
      for (const w of p.workstreams ?? []) {
        if (w.sessions?.length) {
          ref = { p: p.id, w: w.id, s: w.sessions[0].id };
          break;
        }
      }
      if (ref) break;
    }
    expect(ref, 'demo seed should include a session to open').toBeTruthy();

    const opened = await request.post(
      `/api/projects/${ref!.p}/workstreams/${ref!.w}/sessions/${ref!.s}/open`,
    );
    expect(opened.status()).toBe(200);
    expect((await opened.json()).mode).toBe('terminal');
  });

  test('opening an unknown session is a 404', async ({ request }) => {
    const projectId = await createProject(request, 'hs-prov-e2e-');
    const created = await request.post(
      `/api/projects/${projectId}/workstreams`,
      {
        data: {
          providerId: 'fake',
          name: 'openable',
          config: { branch: 'main' },
        },
      },
    );
    const ws = await created.json();
    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${ws.id}/sessions/ghost/open`,
    );
    expect(res.status()).toBe(404);
  });

  test('rejects a workstream with invalid config', async ({ request }) => {
    const projectId = await createProject(request, 'hs-prov-e2e-');
    const res = await request.post(`/api/projects/${projectId}/workstreams`, {
      data: { providerId: 'fake', config: {} },
    });
    expect(res.status()).toBe(400);
    const body = await res.json();
    expect(body.fields).toBeTruthy();
  });

  test('rejects an unknown provider', async ({ request }) => {
    const projectId = await createProject(request, 'hs-prov-e2e-');
    const res = await request.post(`/api/projects/${projectId}/workstreams`, {
      data: { providerId: 'ghost', config: { branch: 'main' } },
    });
    expect(res.status()).toBe(400);
  });
});
