import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, test, type APIRequestContext } from '@playwright/test';

/**
 * Two sessions in one workstream must not end up wearing the same title.
 *
 * The rule binds the **rename** only, and only when the rename would *introduce*
 * a duplicate. A session's title is otherwise its provider's — two Claude
 * sessions in a workstream are both "claude session" by design — so the daemon
 * refuses the name a user picks, never the one a provider reports, and never the
 * reset that hands a session back to it.
 */

interface StateSession {
  id: string;
  title: string;
}

async function setup(
  request: APIRequestContext,
): Promise<{ projectId: string; workstreamId: string; sessions: string[] }> {
  const dir = await mkdtemp(join(tmpdir(), 'hs-sess-title-'));
  const created = await request.post('/api/projects', { data: { path: dir } });
  expect(created.status()).toBe(201);
  const projectId = (await created.json()).id as string;

  const madeWs = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'fake',
      name: 'titles',
      config: { branch: 'main' },
    },
  });
  expect(madeWs.status()).toBe(201);
  const workstreamId = await poll(request, projectId, (ws) => ws.id);

  // Two sessions from the same option, so they arrive with the same
  // provider-given title — the collision the rule must leave alone.
  for (let i = 0; i < 2; i++) {
    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
      { data: { providerId: 'fake', optionId: 'claude', config: {} } },
    );
    expect(res.status()).toBe(202);
    await pollSessions(request, projectId, i + 1);
  }
  const sessions = (await pollSessions(request, projectId, 2)).map((s) => s.id);
  return { projectId, workstreamId, sessions };
}

async function poll<T>(
  request: APIRequestContext,
  projectId: string,
  pick: (ws: { id: string; sessions: StateSession[] }) => T,
): Promise<T> {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const ws = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find((w: { id: string }) => !w.id.startsWith('creating-'));
    if (ws) return pick(ws);
    await new Promise((r) => setTimeout(r, 40));
  }
  throw new Error('timed out waiting for the workstream');
}

/** Polls until `count` real (non-placeholder) sessions are present. */
async function pollSessions(
  request: APIRequestContext,
  projectId: string,
  count: number,
): Promise<StateSession[]> {
  const deadline = Date.now() + 15_000;
  let last: StateSession[] = [];
  while (Date.now() < deadline) {
    last = await poll(request, projectId, (ws) =>
      (ws.sessions ?? []).filter((s) => !s.id.startsWith('starting-')),
    );
    if (last.length >= count) return last;
    await new Promise((r) => setTimeout(r, 40));
  }
  throw new Error(`timed out waiting for ${count} sessions; saw ${last.length}`);
}

const rename = (
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  sessionId: string,
  title: string,
) =>
  request.patch(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${sessionId}`,
    { data: { title } },
  );

test('two sessions cannot be renamed to the same title', async ({
  request,
}) => {
  const { projectId, workstreamId, sessions } = await setup(request);
  const [first, second] = sessions;

  expect(
    (await rename(request, projectId, workstreamId, first, 'review')).status(),
  ).toBe(204);

  const refused = await rename(
    request,
    projectId,
    workstreamId,
    second,
    'review',
  );
  expect(refused.status()).toBe(409);
  expect((await refused.json()).error).toContain('review');

  // Nothing changed: the second session kept the title it had.
  const titles = (await pollSessions(request, projectId, 2)).map((s) => s.title);
  expect(titles.filter((t) => t === 'review')).toHaveLength(1);
});

test('a title differing only by case is the same title', async ({
  request,
}) => {
  const { projectId, workstreamId, sessions } = await setup(request);
  const [first, second] = sessions;
  await rename(request, projectId, workstreamId, first, 'Review');

  for (const title of ['review', 'REVIEW', '  Review  ']) {
    const res = await rename(request, projectId, workstreamId, second, title);
    expect(res.status(), `renaming to ${JSON.stringify(title)}`).toBe(409);
  }
  expect(
    (await rename(request, projectId, workstreamId, second, 'Review 2')).status(),
  ).toBe(204);
});

// The by-design collision: nothing renamed these, so the provider's own title
// stands on both — and the reset that hands one back to it is never refused, or a
// rename would be a one-way door.
test('provider-given titles may collide, and a reset is always allowed', async ({
  request,
}) => {
  const { projectId, workstreamId, sessions } = await setup(request);
  const before = await pollSessions(request, projectId, 2);
  expect(before[0].title).toBe(before[1].title);

  const [first] = sessions;
  expect(
    (await rename(request, projectId, workstreamId, first, 'temporary')).status(),
  ).toBe(204);

  const reset = await request.delete(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${first}/title`,
  );
  expect(reset.status()).toBe(204);
});

// Sessions in different workstreams are not siblings.
test('the same title in another workstream is fine', async ({ request }) => {
  const first = await setup(request);
  const second = await setup(request);

  expect(
    (
      await rename(
        request,
        first.projectId,
        first.workstreamId,
        first.sessions[0],
        'shared',
      )
    ).status(),
  ).toBe(204);
  expect(
    (
      await rename(
        request,
        second.projectId,
        second.workstreamId,
        second.sessions[0],
        'shared',
      )
    ).status(),
  ).toBe(204);
});
