import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, test, type APIRequestContext } from '@playwright/test';
import { isPlaceholderSession } from '../placeholders';

/**
 * PATCH /api/projects/{id}/workstreams/{wsId}/sessions/{sid} — a session's display
 * name, and DELETE …/{sid}/title, which gives the provider's own name back.
 *
 * The point of the feature is that the name *sticks*: a session's title otherwise
 * belongs to its provider, which reports one with every session it discovers — and
 * the daemon writes those over its projection on every cycle. So the assertion that
 * matters here is the one made after the daemon has re-projected several times.
 */

async function createProject(request: APIRequestContext): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), 'hs-rename-e2e-'));
  const res = await request.post('/api/projects', { data: { path: dir } });
  expect(res.status()).toBe(201);
  return (await res.json()).id as string;
}

async function createWorkstream(
  request: APIRequestContext,
  projectId: string,
): Promise<string> {
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'renames', config: { branch: 'main' } },
  });
  expect(res.status()).toBe(201);
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const real = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find(
        (w: { id: string }) => !String(w.id).startsWith('creating-'),
      );
    if (real) return real.id as string;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`timed out waiting for a workstream in ${projectId}`);
}

async function createSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
): Promise<string> {
  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );
  expect(res.status()).toBe(202);
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    const real = (await sessions(request, projectId, workstreamId)).find(
      (s) => !isPlaceholderSession(s.id),
    );
    if (real) return real.id;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`timed out waiting for a session in ${workstreamId}`);
}

async function sessions(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
): Promise<{ id: string; title: string }[]> {
  const state = await (await request.get('/api/projects')).json();
  return (
    state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find((w: { id: string }) => w.id === workstreamId)
      ?.sessions ?? []
  );
}

async function titleOf(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  sessionId: string,
): Promise<string | undefined> {
  return (await sessions(request, projectId, workstreamId)).find(
    (s) => s.id === sessionId,
  )?.title;
}

/**
 * Waits until the session's title is something other than `was`.
 *
 * The generous timeout is about load, not about the daemon being slow: every poll
 * fetches the *whole* projection, which by the end of a parallel run holds every
 * project the suite has created, so 5s of polling can starve under 16 workers even
 * though the reset itself re-projects synchronously.
 */
async function awaitTitleChange(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  sessionId: string,
  was: string,
  timeoutMs = 15_000,
): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const now = await titleOf(request, projectId, workstreamId, sessionId);
    if (now && now !== was) return now;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`title of ${sessionId} never moved off ${was}`);
}

test.describe('session rename API', () => {
  test('renames a session and keeps the name across reconciliation', async ({
    request,
  }) => {
    const projectId = await createProject(request);
    const workstreamId = await createWorkstream(request, projectId);
    const sessionId = await createSession(request, projectId, workstreamId);
    const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${sessionId}`;

    const res = await request.patch(base, { data: { title: 'Port the CLI' } });
    expect(res.status()).toBe(204);
    expect(await titleOf(request, projectId, workstreamId, sessionId)).toBe(
      'Port the CLI',
    );

    // The fake provider keeps reporting its own title for this session; give it
    // several cycles to try to win, and check it did not.
    await new Promise((r) => setTimeout(r, 1500));
    expect(await titleOf(request, projectId, workstreamId, sessionId)).toBe(
      'Port the CLI',
    );
  });

  test('a second rename replaces the first', async ({ request }) => {
    const projectId = await createProject(request);
    const workstreamId = await createWorkstream(request, projectId);
    const sessionId = await createSession(request, projectId, workstreamId);
    const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${sessionId}`;

    expect(
      (await request.patch(base, { data: { title: 'first' } })).status(),
    ).toBe(204);
    expect(
      (await request.patch(base, { data: { title: 'second' } })).status(),
    ).toBe(204);
    expect(await titleOf(request, projectId, workstreamId, sessionId)).toBe(
      'second',
    );
  });

  test('DELETE …/title hands the session back to its provider', async ({
    request,
  }) => {
    const projectId = await createProject(request);
    const workstreamId = await createWorkstream(request, projectId);
    const sessionId = await createSession(request, projectId, workstreamId);
    const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${sessionId}`;

    expect(
      (await request.patch(base, { data: { title: 'mine' } })).status(),
    ).toBe(204);
    expect((await request.delete(`${base}/title`)).status()).toBe(204);

    // The provider's own title returns on the cycle that reports it.
    const restored = await awaitTitleChange(
      request,
      projectId,
      workstreamId,
      sessionId,
      'mine',
    );
    expect(restored).not.toBe('mine');
    expect(restored).toBeTruthy();

    // Idempotent: clearing a name that is already the provider's succeeds.
    expect((await request.delete(`${base}/title`)).status()).toBe(204);
  });

  test('an empty title is refused rather than silently resetting', async ({
    request,
  }) => {
    const projectId = await createProject(request);
    const workstreamId = await createWorkstream(request, projectId);
    const sessionId = await createSession(request, projectId, workstreamId);
    const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${sessionId}`;

    expect(
      (await request.patch(base, { data: { title: 'kept' } })).status(),
    ).toBe(204);
    const res = await request.patch(base, { data: { title: '  ' } });
    expect(res.status()).toBe(400);
    expect(await titleOf(request, projectId, workstreamId, sessionId)).toBe(
      'kept',
    );
  });

  test('renaming an unknown session is a 404', async ({ request }) => {
    const projectId = await createProject(request);
    const workstreamId = await createWorkstream(request, projectId);

    const ghost = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/ghost`;
    expect(
      (await request.patch(ghost, { data: { title: 'x' } })).status(),
    ).toBe(404);
    expect((await request.delete(`${ghost}/title`)).status()).toBe(404);
  });

  test('a renamed session can still be removed, and its name goes with it', async ({
    request,
  }) => {
    const projectId = await createProject(request);
    const workstreamId = await createWorkstream(request, projectId);
    const sessionId = await createSession(request, projectId, workstreamId);
    const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${sessionId}`;

    expect(
      (await request.patch(base, { data: { title: 'doomed' } })).status(),
    ).toBe(204);
    expect((await request.delete(base)).status()).toBe(204);

    const remaining = await sessions(request, projectId, workstreamId);
    expect(remaining.some((s) => s.id === sessionId)).toBe(false);
    expect(remaining.some((s) => s.title === 'doomed')).toBe(false);
  });
});
