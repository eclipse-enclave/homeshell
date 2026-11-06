import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, test, type APIRequestContext } from '@playwright/test';

/**
 * A session creation that fails leaves the session behind in `error` state, with
 * the provider's reason on the session itself — not on its workstream. The row is
 * the placeholder the user was already watching, so the failure is explained
 * where they were looking, and removing the session is what disposes of it.
 *
 * The fake provider fails on an unknown optionId: the create is accepted (202)
 * and the failure happens in the background, which is the real shape of a build
 * that dies minutes in.
 */

interface StateSession {
  id: string;
  state: string;
  error?: string;
  preview?: string;
}

interface StateWorkstream {
  id: string;
  statusMessage?: string;
  effectiveState: string;
  sessions: StateSession[];
}

async function project(request: APIRequestContext): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), 'hs-session-error-'));
  const res = await request.post('/api/projects', { data: { path: dir } });
  expect(res.status()).toBe(201);
  return (await res.json()).id as string;
}

async function workstreamOf(
  request: APIRequestContext,
  projectId: string,
): Promise<StateWorkstream | undefined> {
  const state = await (await request.get('/api/projects')).json();
  return state.projects
    ?.find((p: { id: string }) => p.id === projectId)
    ?.workstreams?.find((w: { id: string }) => !w.id.startsWith('creating-'));
}

async function workstream(
  request: APIRequestContext,
  projectId: string,
): Promise<string> {
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'fake',
      name: 'session-error',
      config: { branch: 'main' },
    },
  });
  expect(res.status()).toBe(201);
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const ws = await workstreamOf(request, projectId);
    if (ws) return ws.id;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error('timed out waiting for the workstream');
}

/** Polls for the row a failed create leaves behind. */
async function failedSession(
  request: APIRequestContext,
  projectId: string,
): Promise<StateSession> {
  const deadline = Date.now() + 15_000;
  let last: StateSession[] = [];
  while (Date.now() < deadline) {
    last = (await workstreamOf(request, projectId))?.sessions ?? [];
    const failed = last.find((s) => s.state === 'error');
    if (failed) return failed;
    await new Promise((r) => setTimeout(r, 40));
  }
  throw new Error(`timed out waiting for a failed session; saw ${JSON.stringify(last)}`);
}

async function createFailingSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
): Promise<StateSession> {
  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'no-such-agent', config: {} } },
  );
  expect(res.status()).toBe(202);
  return failedSession(request, projectId);
}

test('a failed create leaves the session in error, carrying the reason', async ({
  request,
}) => {
  const projectId = await project(request);
  const workstreamId = await workstream(request, projectId);

  const failed = await createFailingSession(request, projectId, workstreamId);
  expect(failed.error).toContain('no-such-agent');

  // The error belongs to the session, so the workstream says nothing: a copy
  // there would outlive the row the user removes.
  const ws = await workstreamOf(request, projectId);
  expect(ws?.statusMessage ?? '').toBe('');
  // It does surface in the workstream's aggregate, which is how the card gets the
  // user's attention without holding the text.
  expect(ws?.effectiveState).toBe('error');
});

test('removing the failed session disposes of it completely', async ({
  request,
}) => {
  const projectId = await project(request);
  const workstreamId = await workstream(request, projectId);
  const failed = await createFailingSession(request, projectId, workstreamId);

  const removed = await request.delete(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${failed.id}`,
  );
  expect(removed.status()).toBe(204);

  // Gone from the projection every client reads, and it stays gone — no provider
  // reports it, so nothing can bring it back.
  const deadline = Date.now() + 10_000;
  let sessions: StateSession[] = [];
  while (Date.now() < deadline) {
    sessions = (await workstreamOf(request, projectId))?.sessions ?? [];
    if (sessions.length === 0) break;
    await new Promise((r) => setTimeout(r, 40));
  }
  expect(sessions).toEqual([]);

  const ws = await workstreamOf(request, projectId);
  expect(ws?.effectiveState).not.toBe('error');
});

// A failed session is terminal, so it must not make its workstream busy — the
// reason the row could be kept at all rather than discarded like the old
// starting placeholder.
test('a workstream holding a failed session is still removable unforced', async ({
  request,
}) => {
  const projectId = await project(request);
  const workstreamId = await workstream(request, projectId);
  await createFailingSession(request, projectId, workstreamId);

  const removed = await request.delete(
    `/api/projects/${projectId}/workstreams/${workstreamId}`,
  );
  expect(removed.status()).toBe(204);
});
