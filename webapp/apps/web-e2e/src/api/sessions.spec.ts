import { expect, test, type APIRequestContext } from '@playwright/test';
import { isPlaceholderSession } from '../placeholders';
import {
  createProject,
  createWorkstream,
  pollFor,
  sessionsOf,
} from './workstream-helpers';

/**
 * Polls until a real session appears in the workstream — one the provider
 * issued, not a placeholder the daemon holds while the create is in flight
 * ("creating-sess-" for the reservation, "starting-" for the row the user
 * watches).
 */
const awaitRealSession = (
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
) =>
  pollFor(
    `a real session in workstream ${workstreamId}`,
    async () =>
      (await sessionsOf(request, projectId, workstreamId)).find(
        (session) => !isPlaceholderSession(session.id),
      ),
    5000,
  );

/** Reads one session's state from the full-state snapshot. */
const sessionState = async (
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  sessionId: string,
) =>
  (await sessionsOf(request, projectId, workstreamId)).find(
    (session) => session.id === sessionId,
  )?.state;

/**
 * Black-box tests for the session lifecycle against the daemon's public API,
 * driven through the in-memory fake provider (demo mode). UI-free, so they
 * survive a daemon rewrite.
 */
test.describe('session API', () => {
  test('session-schemas lists the provider agents', async ({ request }) => {
    const projectId = await createProject(request, 'hs-sess-e2e-');
    const wsId = await createWorkstream(request, projectId, 'sessions');
    const res = await request.get(
      `/api/projects/${projectId}/workstreams/${wsId}/session-schemas`,
    );
    expect(res.status()).toBe(200);
    const groups = await res.json();
    expect(groups).toHaveLength(1);
    expect(groups[0].providerId).toBe('fake');
    expect(groups[0].label).toBeTruthy();
    // A prefix, not the whole list: the fake gains an agent from time to time and
    // that is not this endpoint's business.
    expect(
      groups[0].options.map((o: { id: string }) => o.id).slice(0, 2),
    ).toEqual(['claude', 'codex']);
    expect(groups[0].options[0].label).toBeTruthy();
    expect(groups[0].options[0].schema).toBeTruthy();
  });

  test('full lifecycle: create → stop → start → remove', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-sess-e2e-');
    const wsId = await createWorkstream(request, projectId, 'sessions');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;

    // Create with a chosen agent + config (returns a placeholder; wait for real).
    const created = await request.post(base, {
      data: {
        providerId: 'fake',
        optionId: 'claude',
        config: { model: 'opus' },
      },
    });
    expect(created.status()).toBe(202);
    const sess = await awaitRealSession(request, projectId, wsId);
    expect(sess.agent).toBe('Claude Code');
    expect(await sessionState(request, projectId, wsId, sess.id)).toBe('idle');

    // Stop → stopped.
    expect((await request.post(`${base}/${sess.id}/stop`)).status()).toBe(204);
    expect(await sessionState(request, projectId, wsId, sess.id)).toBe(
      'stopped',
    );

    // Start → running (the fake resumes to running).
    expect((await request.post(`${base}/${sess.id}/start`)).status()).toBe(204);
    expect(await sessionState(request, projectId, wsId, sess.id)).toBe(
      'running',
    );

    // Remove is allowed in any state — a running session is torn down by its
    // provider, no "stop first" required.
    expect((await request.delete(`${base}/${sess.id}`)).status()).toBe(204);
    expect(
      await sessionState(request, projectId, wsId, sess.id),
    ).toBeUndefined();
  });

  test('rejects a session with config outside the schema (400 with fields)', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-sess-e2e-');
    const wsId = await createWorkstream(request, projectId, 'sessions');
    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${wsId}/sessions`,
      {
        data: {
          providerId: 'fake',
          optionId: 'claude',
          config: { model: 'gpt-9' },
        },
      },
    );
    expect(res.status()).toBe(400);
    expect((await res.json()).fields).toBeTruthy();
  });

  // A projection can list a real session before its create has promoted the
  // placeholder; the promotion must not then list it a second time.
  test('lists each session once while two are created', async ({ request }) => {
    const projectId = await createProject(request, 'hs-sess-e2e-');
    const wsId = await createWorkstream(request, projectId, 'sessions');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    for (let i = 0; i < 2; i++) {
      const res = await request.post(base, {
        data: { providerId: 'fake', optionId: 'claude', config: {} },
      });
      expect(res.status()).toBe(202);
    }

    await pollFor('both real sessions', async () => {
      const ids = (await sessionsOf(request, projectId, wsId)).map(
        (session) => session.id,
      );
      expect(new Set(ids).size).toBe(ids.length);
      return ids.length === 2 && !ids.some(isPlaceholderSession)
        ? ids
        : undefined;
    });
  });

  test('enforces the concurrency limit with a clear 409', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-sess-e2e-');
    const wsId = await createWorkstream(request, projectId, 'sessions');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;

    // The demo fake declares MaxConcurrentSessions=3.
    // Each creation is async; wait for the real session count to increase
    // before the next so the concurrency counter sees it.
    for (let i = 0; i < 3; i++) {
      expect(
        (
          await request.post(base, {
            data: { providerId: 'fake', optionId: 'claude' },
          })
        ).status(),
      ).toBe(202);
      await pollFor(
        `${i + 1} sessions`,
        async () =>
          (await sessionsOf(request, projectId, wsId)).length > i
            ? true
            : undefined,
        5000,
      );
    }
    const rejected = await request.post(base, {
      data: { providerId: 'fake', optionId: 'claude' },
    });
    expect(rejected.status()).toBe(409);
    expect((await rejected.json()).error).toContain('concurrent');
  });
});
