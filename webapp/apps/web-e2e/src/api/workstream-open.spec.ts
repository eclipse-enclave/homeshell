import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, test, type APIRequestContext } from '@playwright/test';
import { isPlaceholderSession } from '../placeholders';

/**
 * The workstream hand-off: `POST …/open` (the default open, no body) and
 * `POST …/open-with` (one of the modes the daemon advertises).
 */

async function createProject(request: APIRequestContext): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), 'hs-wsopen-e2e-'));
  const res = await request.post('/api/projects', { data: { path: dir } });
  expect(res.status()).toBe(201);
  return (await res.json()).id as string;
}

/** Polls until the asynchronously-created (non-placeholder) workstream lands. */
async function awaitRealWorkstream(
  request: APIRequestContext,
  projectId: string,
  timeoutMs = 5000,
): Promise<{
  id: string;
  workingDir?: string;
  openModes?: {
    id: string;
    label: string;
    opensInBrowser?: boolean;
    default?: boolean;
  }[];
}> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const project = state.projects?.find(
      (p: { id: string }) => p.id === projectId,
    );
    const real = project?.workstreams?.find(
      (w: { id: string }) => !String(w.id).startsWith('creating-'),
    );
    if (real) return real;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`timed out waiting for a workstream in project ${projectId}`);
}

/** Polls until the asynchronously-created session lands in the workstream. */
async function awaitRealSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  timeoutMs = 5000,
): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const workstream = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find((w: { id: string }) => w.id === workstreamId);
    const real = workstream?.sessions?.find(
      (s: { id: string }) => !isPlaceholderSession(s.id),
    );
    if (real) return real.id as string;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(
    `timed out waiting for a session in workstream ${workstreamId}`,
  );
}

test('a live containerized session is its own open mode; the default open stays on the host', async ({
  request,
}) => {
  const projectId = await createProject(request);
  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'shellable', config: { branch: 'main' } },
  });
  expect(created.status()).toBe(201);
  const workstream = await awaitRealWorkstream(request, projectId);

  const started = await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude' } },
  );
  expect(started.status()).toBe(202);
  const sessionId = await awaitRealSession(request, projectId, workstream.id);

  const shell = await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/open-with`,
    { data: { mode: `session-shell:${sessionId}` } },
  );
  expect(shell.status()).toBe(200);
  expect((await shell.json()).data.target).toBe('container');

  // The fake never creates the workstream's folder, so a default open that
  // stays on the host has nowhere to go; one that went into the container would
  // succeed.
  const open = await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/open`,
  );
  expect(open.status()).toBe(409);
  expect((await open.json()).error).toContain('working directory');

  // A body is ignored rather than dispatched: choosing a mode is open-with's job.
  // Dispatched, it would open the container (200); ignored, it is the host
  // open's 409.
  const withBody = await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/open`,
    { data: { mode: `session-shell:${sessionId}` } },
  );
  expect(withBody.status()).toBe(409);
});

test("a session's shell is refused once the session has no container", async ({
  request,
}) => {
  const projectId = await createProject(request);
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'noshell', config: { branch: 'main' } },
  });
  const workstream = await awaitRealWorkstream(request, projectId);
  await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude' } },
  );
  const sessionId = await awaitRealSession(request, projectId, workstream.id);
  const base = `/api/projects/${projectId}/workstreams/${workstream.id}`;
  expect((await request.post(`${base}/sessions/${sessionId}/stop`)).status()).toBe(204);

  // The user asked for that container, so there is no fallback to the host.
  const stopped = await request.post(`${base}/open-with`, {
    data: { mode: `session-shell:${sessionId}` },
  });
  expect(stopped.status()).toBe(409);

  // A session the workstream no longer has, removed since the menu was drawn:
  // stale state, not a bad request.
  const ghost = await request.post(`${base}/open-with`, {
    data: { mode: 'session-shell:ghost' },
  });
  expect(ghost.status()).toBe(409);
});

test('refuses to open a workstream that is not materialized on disk', async ({
  request,
}) => {
  const projectId = await createProject(request);
  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'openable', config: { branch: 'main' } },
  });
  expect(created.status()).toBe(201);
  // The fake provider records a working directory but never creates it.
  const workstream = await awaitRealWorkstream(request, projectId);
  expect(workstream.workingDir).toBeTruthy();

  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/open`,
  );
  expect(res.status()).toBe(409);
  expect((await res.json()).error).toContain('working directory');
});

test('opening an unknown workstream is a 404', async ({ request }) => {
  const projectId = await createProject(request);

  const open = await request.post(
    `/api/projects/${projectId}/workstreams/ghost/open`,
  );
  expect(open.status()).toBe(404);

  const openWith = await request.post(
    `/api/projects/${projectId}/workstreams/ghost/open-with`,
    { data: { mode: 'open-with:terminal' } },
  );
  expect(openWith.status()).toBe(404);
});

test('advertises the open modes each workstream supports', async ({
  request,
}) => {
  const projectId = await createProject(request);
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'modes', config: { branch: 'main' } },
  });
  const workstream = await awaitRealWorkstream(request, projectId);

  // The fake never materializes its directory and there is no session yet.
  expect(workstream.openModes).toEqual([]);

  await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude' } },
  );
  const sessionId = await awaitRealSession(request, projectId, workstream.id);

  const withSession = await awaitRealWorkstream(request, projectId);
  const ids = (withSession.openModes ?? []).map((mode) => mode.id);
  // A shell in the session's container; the host terminal needs the folder,
  // which the fake never creates, so nothing is the plain open's default.
  expect(ids).toContain(`session-shell:${sessionId}`);
  expect(ids).not.toContain('open-with:terminal');
  expect((withSession.openModes ?? []).some((mode) => mode.default)).toBe(false);
  expect(ids).toContain('link:web-ide');
  // A browser-bound mode says so, so a client can open the tab inside the
  // click rather than after the response arrives.
  const link = (withSession.openModes ?? []).find((mode) => mode.id === 'link:web-ide');
  expect(link?.opensInBrowser).toBe(true);
  const shell = (withSession.openModes ?? []).find(
    (mode) => mode.id === `session-shell:${sessionId}`,
  );
  expect(shell?.opensInBrowser ?? false).toBe(false);
  for (const mode of withSession.openModes ?? []) {
    expect(mode.label).toBeTruthy();
  }
});

test('a provider-declared link resolves to a URL for the client to open', async ({
  request,
}) => {
  const projectId = await createProject(request);
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'weblink', config: { branch: 'main' } },
  });
  const workstream = await awaitRealWorkstream(request, projectId);
  await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude' } },
  );
  await awaitRealSession(request, projectId, workstream.id);

  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/open-with`,
    { data: { mode: 'link:web-ide' } },
  );
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.mode).toBe('link:web-ide');
  expect(body.data.target).toBe('browser');
  expect(body.data.url).toMatch(/^https?:\/\//);
});

test('a link no live session declares is refused', async ({ request }) => {
  const projectId = await createProject(request);
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'nolink', config: { branch: 'main' } },
  });
  const workstream = await awaitRealWorkstream(request, projectId);

  // No session declares the link, so this is a conflict, not a bad request.
  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/open-with`,
    { data: { mode: 'link:web-ide' } },
  );
  expect(res.status()).toBe(409);
});

test('open-with requires a mode', async ({ request }) => {
  const projectId = await createProject(request);
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'nomode', config: { branch: 'main' } },
  });
  const workstream = await awaitRealWorkstream(request, projectId);

  for (const data of [undefined, {}, { mode: '  ' }]) {
    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${workstream.id}/open-with`,
      data === undefined ? undefined : { data },
    );
    expect(res.status(), JSON.stringify(data)).toBe(400);
  }
});

// A live session declares links but not this id, so the id is the bad part of
// the request rather than the workstream's state.
test('a link id no session declares is a 400', async ({ request }) => {
  const projectId = await createProject(request);
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'badlink', config: { branch: 'main' } },
  });
  const workstream = await awaitRealWorkstream(request, projectId);
  await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude' } },
  );
  await awaitRealSession(request, projectId, workstream.id);

  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/open-with`,
    { data: { mode: 'link:dashboard' } },
  );
  expect(res.status()).toBe(400);
});

test('an unknown open mode is a 400', async ({ request }) => {
  const projectId = await createProject(request);
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'badmode', config: { branch: 'main' } },
  });
  const workstream = await awaitRealWorkstream(request, projectId);

  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/open-with`,
    { data: { mode: 'telepathy' } },
  );
  expect(res.status()).toBe(400);
});
