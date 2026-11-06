import { type APIRequestContext } from '@playwright/test';
import { expect, test } from '../fixtures';
import { isPlaceholderSession } from '../placeholders';
import { awaitRealWorkstream, createProject } from './workstream-helpers';

/**
 * Black-box cover for the removal guard: a workstream with a running session is
 * refused, and the same call with force tears it down. The guard is the
 * daemon's, so these go straight at the API — no browser, no UI confirmation in
 * the way — which is exactly the case a UI-side check would not cover.
 */

async function snapshot(request: APIRequestContext) {
  return (await (await request.get('/api/projects')).json()) as {
    projects?: {
      id: string;
      workstreams?: {
        id: string;
        sessions?: { id: string; state: string }[];
      }[];
    }[];
  };
}

/** Polls until a non-placeholder session appears; returns its ID. */
async function awaitRealSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  timeoutMs = 15000,
): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const state = await snapshot(request);
    const real = state.projects
      ?.find((p) => p.id === projectId)
      ?.workstreams?.find((w) => w.id === workstreamId)
      ?.sessions?.find((s) => !isPlaceholderSession(s.id));
    if (real) return real.id;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(
    `timed out waiting for a session in workstream ${workstreamId}`,
  );
}

async function workstreamOf(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
) {
  const state = await snapshot(request);
  return state.projects
    ?.find((p) => p.id === projectId)
    ?.workstreams?.find((w) => w.id === workstreamId);
}

/**
 * A project with one workstream holding one **running** session. The fake
 * provider creates sessions idle, so stop→start is what puts one in the state
 * the guard cares about.
 */
async function projectWithRunningSession(request: APIRequestContext) {
  const projectId = await createProject(request, 'hs-wsrm-e2e-');
  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'removal', config: { branch: 'main' } },
  });
  expect(created.status()).toBe(201);
  const { id: workstreamId } = await awaitRealWorkstream(request, projectId);

  const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`;
  expect(
    (
      await request.post(base, {
        data: { providerId: 'fake', optionId: 'claude' },
      })
    ).status(),
  ).toBe(202);
  const sessionId = await awaitRealSession(request, projectId, workstreamId);
  expect((await request.post(`${base}/${sessionId}/stop`)).status()).toBe(204);
  expect((await request.post(`${base}/${sessionId}/start`)).status()).toBe(204);

  const ws = await workstreamOf(request, projectId, workstreamId);
  expect(ws?.sessions?.find((s) => s.id === sessionId)?.state).toBe('running');
  return { projectId, workstreamId, sessionId };
}

test.describe('workstream removal guard', () => {
  test('a running session makes removal a 409 that names it, and changes nothing', async ({
    request,
  }) => {
    const { projectId, workstreamId, sessionId } =
      await projectWithRunningSession(request);

    const refused = await request.delete(
      `/api/projects/${projectId}/workstreams/${workstreamId}`,
    );
    expect(refused.status()).toBe(409);
    const body = await refused.json();
    // Machine-readable reason first: a client branches on this, not the prose.
    // Spelled out rather than imported from the app: this suite tests the wire
    // contract black-box, so sharing a constant with the client would let both
    // sides drift together without the test noticing.
    expect(body.reason).toBe('sessionsRunning');
    expect(body.error).toBeTruthy();
    expect(body.sessions).toHaveLength(1);
    expect(body.sessions[0].id).toBe(sessionId);
    expect(body.sessions[0].state).toBe('running');

    // The workstream and its session are untouched.
    const ws = await workstreamOf(request, projectId, workstreamId);
    expect(ws).toBeTruthy();
    expect(ws?.sessions?.find((s) => s.id === sessionId)?.state).toBe(
      'running',
    );
  });

  test('the same call with force tears the workstream down', async ({
    request,
  }) => {
    const { projectId, workstreamId } =
      await projectWithRunningSession(request);

    expect(
      (
        await request.delete(
          `/api/projects/${projectId}/workstreams/${workstreamId}`,
        )
      ).status(),
    ).toBe(409);

    const forced = await request.delete(
      `/api/projects/${projectId}/workstreams/${workstreamId}?force=true`,
    );
    expect(forced.status()).toBe(204);
    expect(
      await workstreamOf(request, projectId, workstreamId),
    ).toBeUndefined();
  });

  test('force travels in the body too, beside keepFiles', async ({
    request,
  }) => {
    const { projectId, workstreamId } =
      await projectWithRunningSession(request);

    const forced = await request.delete(
      `/api/projects/${projectId}/workstreams/${workstreamId}`,
      { data: { keepFiles: true, force: true } },
    );
    expect(forced.status()).toBe(204);
    expect(
      await workstreamOf(request, projectId, workstreamId),
    ).toBeUndefined();
  });

  test('a quiescent workstream still removes without force', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-wsrm-e2e-');
    expect(
      (
        await request.post(`/api/projects/${projectId}/workstreams`, {
          data: {
            providerId: 'fake',
            name: 'quiet',
            config: { branch: 'main' },
          },
        })
      ).status(),
    ).toBe(201);
    const { id: workstreamId } = await awaitRealWorkstream(request, projectId);

    // No sessions at all: the guard must not turn every delete into two steps.
    expect(
      (
        await request.delete(
          `/api/projects/${projectId}/workstreams/${workstreamId}`,
        )
      ).status(),
    ).toBe(204);
    expect(
      await workstreamOf(request, projectId, workstreamId),
    ).toBeUndefined();
  });

  test('an idle session does not block removal', async ({ request }) => {
    const projectId = await createProject(request, 'hs-wsrm-e2e-');
    expect(
      (
        await request.post(`/api/projects/${projectId}/workstreams`, {
          data: {
            providerId: 'fake',
            name: 'idle-sessions',
            config: { branch: 'main' },
          },
        })
      ).status(),
    ).toBe(201);
    const { id: workstreamId } = await awaitRealWorkstream(request, projectId);
    const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`;
    expect(
      (
        await request.post(base, {
          data: { providerId: 'fake', optionId: 'claude' },
        })
      ).status(),
    ).toBe(202);
    const sessionId = await awaitRealSession(request, projectId, workstreamId);
    const ws = await workstreamOf(request, projectId, workstreamId);
    expect(ws?.sessions?.find((s) => s.id === sessionId)?.state).toBe('idle');

    expect(
      (
        await request.delete(
          `/api/projects/${projectId}/workstreams/${workstreamId}`,
        )
      ).status(),
    ).toBe(204);
  });
});
