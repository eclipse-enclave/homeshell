import { expect, test, type APIRequestContext } from '@playwright/test';
import { isPlaceholderSession } from '../placeholders';
import { createProject, createWorkstream } from './workstream-helpers';

const PNG = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3]);
const JPEG = Buffer.from([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3]);
const TEN_MIB = 10 * 1024 * 1024;

type WireSession = {
  id: string;
  preview?: string;
  capabilities?: { attachImage?: boolean };
};

/** Reads one session from the full-state snapshot. */
async function findSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  sessionId?: string,
): Promise<WireSession | undefined> {
  const state = await (await request.get('/api/projects')).json();
  const ws = state.projects
    ?.find((p: { id: string }) => p.id === projectId)
    ?.workstreams?.find((w: { id: string }) => w.id === workstreamId);
  return ws?.sessions?.find((s: WireSession) =>
    sessionId ? s.id === sessionId : !isPlaceholderSession(s.id),
  );
}

/** Creates a demo session (async 202) and waits for the real one; returns its id. */
async function createSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
): Promise<string> {
  const created = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: { model: 'opus' } } },
  );
  expect(created.status()).toBe(202);
  let sessionId: string | undefined;
  await expect
    .poll(async () => {
      sessionId = (await findSession(request, projectId, workstreamId))?.id;
      return sessionId;
    })
    .toBeTruthy();
  return sessionId as string;
}

/** A fresh demo session and the base URL of its endpoints. */
async function demoSession(request: APIRequestContext) {
  const projectId = await createProject(request, 'hs-image-e2e-');
  const workstreamId = await createWorkstream(request, projectId, 'image');
  const sessionId = await createSession(request, projectId, workstreamId);
  const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${sessionId}`;
  return { projectId, workstreamId, sessionId, base };
}

/**
 * Black-box tests for attaching an image to a session (`POST {s}/image`),
 * driven through the demo (fake) provider: it implements attaching, makes up an
 * image inbox path and echoes it into the session's preview. UI-free, so these
 * survive a daemon rewrite.
 */
test.describe('session image API', () => {
  test('a demo session declares attachImage', async ({ request }) => {
    const { projectId, workstreamId, sessionId } = await demoSession(request);
    const session = await findSession(request, projectId, workstreamId, sessionId);
    expect(session?.capabilities?.attachImage).toBe(true);
  });

  for (const [contentType, body, extension] of [
    ['image/png', PNG, '.png'],
    ['image/jpeg', JPEG, '.jpg'],
  ] as const) {
    test(`POST image as ${contentType} returns 200 with its path and shows it in the preview`, async ({
      request,
    }) => {
      const { projectId, workstreamId, sessionId, base } = await demoSession(request);

      const res = await request.post(`${base}/image`, {
        headers: { 'content-type': contentType },
        data: body,
      });
      expect(res.status()).toBe(200);
      const { path } = (await res.json()) as { path: string };
      expect(path).toMatch(/^\/mnt\/host-images\//);
      expect(path.endsWith(extension)).toBe(true);

      await expect
        .poll(async () => (await findSession(request, projectId, workstreamId, sessionId))?.preview)
        .toContain(`[image: ${path}]`);
    });
  }

  test('POST image with a content type other than PNG or JPEG returns 400', async ({
    request,
  }) => {
    const { base } = await demoSession(request);
    const res = await request.post(`${base}/image`, {
      headers: { 'content-type': 'image/gif' },
      data: Buffer.from('GIF89a'),
    });
    expect(res.status()).toBe(400);
    expect((await res.json()).error).toBeTruthy();
  });

  test('POST image whose bytes do not match its content type returns 400', async ({
    request,
  }) => {
    const { base } = await demoSession(request);
    const res = await request.post(`${base}/image`, {
      headers: { 'content-type': 'image/png' },
      data: JPEG,
    });
    expect(res.status()).toBe(400);
    expect((await res.json()).error).toBeTruthy();
  });

  test('POST image over 10 MiB returns 413', async ({ request }) => {
    const { base } = await demoSession(request);
    const res = await request.post(`${base}/image`, {
      headers: { 'content-type': 'image/png' },
      data: Buffer.concat([PNG, Buffer.alloc(TEN_MIB)]),
    });
    expect(res.status()).toBe(413);
    expect((await res.json()).error).toBeTruthy();
  });

  test('POST image of exactly 10 MiB is accepted', async ({ request }) => {
    const { base } = await demoSession(request);
    const res = await request.post(`${base}/image`, {
      headers: { 'content-type': 'image/png' },
      data: Buffer.concat([PNG, Buffer.alloc(TEN_MIB - PNG.length)]),
    });
    expect(res.status()).toBe(200);
  });

  test('POST image on a stopped session returns 409 until it is started', async ({
    request,
  }) => {
    const { base } = await demoSession(request);
    expect((await request.post(`${base}/stop`)).status()).toBe(204);

    const res = await request.post(`${base}/image`, {
      headers: { 'content-type': 'image/png' },
      data: PNG,
    });
    expect(res.status()).toBe(409);
    expect((await res.json()).error).toBeTruthy();

    expect((await request.post(`${base}/start`)).status()).toBe(204);
    const again = await request.post(`${base}/image`, {
      headers: { 'content-type': 'image/png' },
      data: PNG,
    });
    expect(again.status()).toBe(200);
  });

  test('POST image without the API token is 401', async ({ request, baseURL }) => {
    const { base } = await demoSession(request);
    const res = await fetch(`${baseURL}${base}/image`, {
      method: 'POST',
      headers: { 'content-type': 'image/png' },
      body: PNG,
    });
    expect(res.status).toBe(401);
  });

  test('POST image from a foreign Origin is 403', async ({ request }) => {
    const { base } = await demoSession(request);
    const res = await request.post(`${base}/image`, {
      headers: { 'content-type': 'image/png', Origin: 'http://evil.com' },
      data: PNG,
    });
    expect(res.status()).toBe(403);
  });
});
