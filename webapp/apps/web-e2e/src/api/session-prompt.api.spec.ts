import { expect, test, type APIRequestContext } from '@playwright/test';
import { isPlaceholderSession } from '../placeholders';
import { createProject, createWorkstream } from './workstream-helpers';

/** Reads one session's preview from the full-state snapshot. */
async function sessionPreview(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  sessionId: string,
): Promise<string | undefined> {
  const state = await (await request.get('/api/projects')).json();
  const ws = state.projects
    .find((p: { id: string }) => p.id === projectId)
    ?.workstreams.find((w: { id: string }) => w.id === workstreamId);
  return ws?.sessions.find((s: { id: string }) => s.id === sessionId)?.preview;
}

/** Polls GET /api/projects until the workstream has a session; returns its id. */
async function awaitRealSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  timeoutMs = 5000,
): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const ws = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find((w: { id: string }) => w.id === workstreamId);
    // Skip the starting placeholder: the daemon projects one while the provider
    // brings the session up, and it is not something to act on.
    const sess = ws?.sessions?.find(
      (candidate: { id: string }) => !isPlaceholderSession(candidate.id),
    );
    if (sess) return sess.id as string;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(
    `timed out waiting for a session in workstream ${workstreamId}`,
  );
}

/** Creates a session (async 202), waits for it, then starts it (→ running). */
async function createRunningSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
): Promise<string> {
  const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`;
  const created = await request.post(base, {
    data: { providerId: 'fake', optionId: 'claude', config: { model: 'opus' } },
  });
  expect(created.status()).toBe(202);
  const sessionId = await awaitRealSession(request, projectId, workstreamId);
  // The fake resumes a started session to `running`.
  expect((await request.post(`${base}/${sessionId}/start`)).status()).toBe(204);
  return sessionId;
}

/**
 * Black-box tests for the one-off prompt endpoint and the wire model's
 * `preview` field, driven through the in-memory fake provider (demo mode). The
 * fake implements Prompter, so it echoes a prompt into the target session's
 * preview. UI-free, so these survive a daemon rewrite.
 */
test.describe('session prompt API', () => {
  test('POST prompt on a running session returns 204 and echoes into preview', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-prompt-e2e-');
    const wsId = await createWorkstream(request, projectId, 'prompt');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const sessionId = await createRunningSession(request, projectId, wsId);

    const res = await request.post(`${base}/${sessionId}/prompt`, {
      data: { text: 'run tests' },
    });
    expect(res.status()).toBe(204);

    // The fake appends "> <text>" to the session's preview and broadcasts;
    // poll the projected state until the echo is observable.
    const deadline = Date.now() + 5000;
    let preview: string | undefined;
    while (Date.now() < deadline) {
      preview = await sessionPreview(request, projectId, wsId, sessionId);
      if (typeof preview === 'string' && preview.includes('run tests')) break;
      await new Promise((r) => setTimeout(r, 50));
    }
    expect(typeof preview).toBe('string');
    expect(preview).toContain('> run tests');
  });

  test('POST prompt with whitespace-only text returns 400', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-prompt-e2e-');
    const wsId = await createWorkstream(request, projectId, 'prompt');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const sessionId = await createRunningSession(request, projectId, wsId);

    const res = await request.post(`${base}/${sessionId}/prompt`, {
      data: { text: '   ' },
    });
    expect(res.status()).toBe(400);
    expect((await res.json()).error).toBeTruthy();
  });

  test('POST prompt with an invalid JSON body returns 400', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-prompt-e2e-');
    const wsId = await createWorkstream(request, projectId, 'prompt');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const sessionId = await createRunningSession(request, projectId, wsId);

    const res = await request.post(`${base}/${sessionId}/prompt`, {
      headers: { 'content-type': 'application/json' },
      data: 'not json',
    });
    expect(res.status()).toBe(400);
    expect((await res.json()).error).toBeTruthy();
  });

  test('POST prompt with an oversized body returns 413', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-prompt-e2e-');
    const wsId = await createWorkstream(request, projectId, 'prompt');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const sessionId = await createRunningSession(request, projectId, wsId);

    // 64 KiB is the cap; send well past it as a single well-formed value.
    const res = await request.post(`${base}/${sessionId}/prompt`, {
      data: { text: 'x'.repeat(128 * 1024) },
    });
    expect(res.status()).toBe(413);
    expect((await res.json()).error).toBeTruthy();
  });

  test('POST prompt on a stopped session returns 409 until it is started', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-prompt-e2e-');
    const wsId = await createWorkstream(request, projectId, 'prompt');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const sessionId = await createRunningSession(request, projectId, wsId);

    expect((await request.post(`${base}/${sessionId}/stop`)).status()).toBe(
      204,
    );
    const res = await request.post(`${base}/${sessionId}/prompt`, {
      data: { text: 'run tests' },
    });
    expect(res.status()).toBe(409);
    expect((await res.json()).error).toBeTruthy();

    expect((await request.post(`${base}/${sessionId}/start`)).status()).toBe(
      204,
    );
    const again = await request.post(`${base}/${sessionId}/prompt`, {
      data: { text: 'run tests' },
    });
    expect(again.status()).toBe(204);
  });

  test('POST key on a running session returns 204 and echoes into preview', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-prompt-e2e-');
    const wsId = await createWorkstream(request, projectId, 'prompt');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const sessionId = await createRunningSession(request, projectId, wsId);

    const res = await request.post(`${base}/${sessionId}/key`, {
      data: { key: 'down' },
    });
    expect(res.status()).toBe(204);

    const deadline = Date.now() + 5000;
    let preview: string | undefined;
    while (Date.now() < deadline) {
      preview = await sessionPreview(request, projectId, wsId, sessionId);
      if (typeof preview === 'string' && preview.includes('[key: down]')) break;
      await new Promise((r) => setTimeout(r, 50));
    }
    expect(preview).toContain('[key: down]');
  });

  test('POST key with an unknown key returns 400', async ({ request }) => {
    const projectId = await createProject(request, 'hs-prompt-e2e-');
    const wsId = await createWorkstream(request, projectId, 'prompt');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const sessionId = await createRunningSession(request, projectId, wsId);

    const res = await request.post(`${base}/${sessionId}/key`, {
      data: { key: 'ctrl-c' },
    });
    expect(res.status()).toBe(400);
    expect((await res.json()).error).toBeTruthy();
  });

  test('POST key on a stopped session returns 409', async ({ request }) => {
    const projectId = await createProject(request, 'hs-prompt-e2e-');
    const wsId = await createWorkstream(request, projectId, 'prompt');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const sessionId = await createRunningSession(request, projectId, wsId);

    expect((await request.post(`${base}/${sessionId}/stop`)).status()).toBe(
      204,
    );
    const res = await request.post(`${base}/${sessionId}/key`, {
      data: { key: 'enter' },
    });
    expect(res.status()).toBe(409);
    expect((await res.json()).error).toBeTruthy();
  });

  test('sessions in GET /api/projects expose a string preview field', async ({
    request,
  }) => {
    // The demo simulator cycles a session (with a canned terminal tail) into
    // the seeded workstream; poll the state until such a session appears and
    // assert the wire model carries `preview` as a non-empty string.
    const deadline = Date.now() + 10000;
    let preview: string | undefined;
    while (Date.now() < deadline) {
      const state = await (await request.get('/api/projects')).json();
      for (const project of state.projects ?? []) {
        for (const ws of project.workstreams ?? []) {
          for (const session of ws.sessions ?? []) {
            if (session.preview !== undefined) {
              // When present, `preview` is always a string.
              expect(typeof session.preview).toBe('string');
              if (session.preview.length > 0) preview = session.preview;
            }
          }
        }
      }
      if (preview) break;
      await new Promise((r) => setTimeout(r, 100));
    }
    expect(
      preview,
      'demo simulator should surface a session with a non-empty preview',
    ).toBeTruthy();
  });
});
