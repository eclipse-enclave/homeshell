import { expect, test } from '@playwright/test';
import { execFile } from 'node:child_process';
import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, join } from 'node:path';
import { promisify } from 'node:util';
import { authHeader } from '../api-token';

const execFileAsync = promisify(execFile);

async function createTempDir(): Promise<string> {
  return mkdtemp(join(tmpdir(), 'hs-e2e-'));
}

/** Turns dir into a git repository, isolated from the developer's git config. */
async function gitInit(dir: string): Promise<void> {
  await execFileAsync('git', ['init', '-q'], {
    cwd: dir,
    env: {
      ...process.env,
      GIT_CONFIG_GLOBAL: '/dev/null',
      GIT_CONFIG_SYSTEM: '/dev/null',
    },
  });
}

/**
 * Black-box tests against the daemon's public HTTP API — deliberately free
 * of any UI so they keep working even if the daemon is rewritten.
 */
test.describe('daemon HTTP API', () => {
  test('GET /api/hello returns the greeting and a version', async ({
    request,
  }) => {
    const res = await request.get('/api/hello');
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('application/json');

    const body = await res.json();
    expect(body.message).toContain('HomeShell');
    expect(typeof body.version).toBe('string');
    expect(body.version.length).toBeGreaterThan(0);
  });

  test('rejects non-GET methods on /api/hello', async ({ request }) => {
    const res = await request.post('/api/hello');
    expect(res.status()).toBe(405);
  });

  test('unknown API routes return 404 instead of the SPA fallback', async ({
    request,
  }) => {
    const res = await request.get('/api/does-not-exist');
    expect(res.status()).toBe(404);
  });

  test('GET /api/projects returns projects with daemon-derived states', async ({
    request,
  }) => {
    const res = await request.get('/api/projects');
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('application/json');

    const body = await res.json();
    expect(Array.isArray(body.projects)).toBe(true);
    expect(body.projects.length).toBeGreaterThanOrEqual(5);
    expect(body.projects.map((p: { name: string }) => p.name)).toContain(
      'homeshell',
    );
    // Specs running in parallel create sessions in the seeded projects, so any
    // aggregate state can show up, "starting" included.
    const states = ['idle', 'starting', 'running', 'waiting', 'unknown', 'error', 'stopped', 'done'];
    for (const project of body.projects) {
      expect(states).toContain(project.state);
      expect(typeof project.runtimeState).toBe('string');
      expect(typeof project.effectiveState).toBe('string');
      expect(Array.isArray(project.workstreams)).toBe(true);
    }
  });

  test('GET /api/events streams a snapshot followed by pushed updates', async ({
    baseURL,
  }) => {
    // Playwright's request fixture buffers whole bodies and SSE never ends,
    // so read the stream with Node's fetch directly.
    const controller = new AbortController();
    const res = await fetch(`${baseURL}/api/events`, {
      signal: controller.signal,
      headers: { accept: 'text/event-stream', ...authHeader() },
    });
    try {
      expect(res.status).toBe(200);
      expect(res.headers.get('content-type')).toContain('text/event-stream');

      const events = await readSseEvents(res.body!, 2, 15_000);

      expect(events[0].type).toBe('state');
      const snapshot = JSON.parse(events[0].data);
      expect(Array.isArray(snapshot.projects)).toBe(true);

      // With the demo simulator running, a state push arrives within
      // seconds; an idle daemon sends a heartbeat instead; and when the in-app
      // notification style is active (a notification spec sharing this daemon
      // may have enabled it) a `notification` frame is another valid push. Any
      // of them proves the connection stays alive and pushes without polling.
      expect(['state', 'heartbeat', 'notification']).toContain(events[1].type);
    } finally {
      controller.abort();
    }
  });

  test('POST /api/projects creates a project from a directory', async ({
    request,
  }) => {
    const path = await createTempDir();
    const res = await request.post('/api/projects', {
      data: { path },
    });
    expect(res.status()).toBe(201);
    expect(res.headers()['content-type']).toContain('application/json');

    const project = await res.json();
    expect(project.id).toBeTruthy();
    expect(project.path).toBe(path);
    expect(project.state).toBe('idle');
    expect(Array.isArray(project.workstreams)).toBe(true);
  });

  test('POST /api/projects defaults name to directory basename', async ({
    request,
  }) => {
    const path = await createTempDir();
    const res = await request.post('/api/projects', {
      data: { path },
    });
    const project = await res.json();
    expect(project.name).toBe(basename(path));
  });

  test('POST /api/projects accepts an explicit name', async ({ request }) => {
    const path = await createTempDir();
    const res = await request.post('/api/projects', {
      data: { path, name: 'My Custom Name' },
    });
    expect(res.status()).toBe(201);
    const project = await res.json();
    expect(project.name).toBe('My Custom Name');
  });

  test('POST /api/projects detects a git-backed directory', async ({
    request,
  }) => {
    const path = await createTempDir();
    await gitInit(path);
    const res = await request.post('/api/projects', { data: { path } });
    expect(res.status()).toBe(201);
    const project = await res.json();
    expect(project.gitBacked).toBe(true);
  });

  test('POST /api/projects marks a plain directory as not git-backed', async ({
    request,
  }) => {
    const path = await createTempDir();
    const res = await request.post('/api/projects', { data: { path } });
    expect(res.status()).toBe(201);
    const project = await res.json();
    expect(project.gitBacked).toBe(false);
  });

  test('POST /api/projects/{id}/refresh re-detects git-backing', async ({
    request,
  }) => {
    // A project added before `git init` starts out not git-backed; after the
    // directory becomes a repository, refresh must pick that up (issue #5).
    const path = await createTempDir();
    const created = await (
      await request.post('/api/projects', { data: { path } })
    ).json();
    expect(created.gitBacked).toBe(false);

    await gitInit(path);
    const res = await request.post(`/api/projects/${created.id}/refresh`);
    expect(res.status()).toBe(204);

    const projects = await (await request.get('/api/projects')).json();
    const refreshed = projects.projects.find(
      (p: { id: string }) => p.id === created.id,
    );
    expect(refreshed.gitBacked).toBe(true);
  });

  test('POST /api/projects/{id}/refresh returns 404 for unknown id', async ({
    request,
  }) => {
    const res = await request.post('/api/projects/nope/refresh');
    expect(res.status()).toBe(404);
  });

  test('POST /api/projects rejects a non-existent path', async ({
    request,
  }) => {
    const res = await request.post('/api/projects', {
      data: { path: '/no/such/directory/zzz' },
    });
    expect(res.status()).toBe(400);
    const body = await res.json();
    expect(body.error).toBeTruthy();
  });

  test('POST /api/projects rejects a duplicate path', async ({ request }) => {
    const path = await createTempDir();
    const first = await request.post('/api/projects', { data: { path } });
    expect(first.status()).toBe(201);
    const second = await request.post('/api/projects', { data: { path } });
    expect(second.status()).toBe(409);
  });

  test('PATCH /api/projects/{id} renames a project', async ({ request }) => {
    const path = await createTempDir();
    const created = await (
      await request.post('/api/projects', { data: { path } })
    ).json();

    const res = await request.patch(`/api/projects/${created.id}`, {
      data: { name: 'Renamed' },
    });
    expect(res.status()).toBe(204);

    const projects = await (await request.get('/api/projects')).json();
    const renamed = projects.projects.find(
      (p: { id: string }) => p.id === created.id,
    );
    expect(renamed.name).toBe('Renamed');
  });

  test('PATCH /api/projects/{id} rejects empty name', async ({ request }) => {
    const res = await request.patch('/api/projects/proj-homeshell', {
      data: { name: '' },
    });
    expect(res.status()).toBe(400);
  });

  test('PATCH /api/projects/{id} returns 404 for unknown id', async ({
    request,
  }) => {
    const res = await request.patch('/api/projects/nope', {
      data: { name: 'x' },
    });
    expect(res.status()).toBe(404);
  });

  test('DELETE /api/projects/{id} removes a project', async ({ request }) => {
    const path = await createTempDir();
    const created = await (
      await request.post('/api/projects', { data: { path } })
    ).json();

    const res = await request.delete(`/api/projects/${created.id}`);
    expect(res.status()).toBe(204);

    const projects = await (await request.get('/api/projects')).json();
    expect(
      projects.projects.find((p: { id: string }) => p.id === created.id),
    ).toBeUndefined();
  });

  test('DELETE /api/projects/{id} returns 404 for unknown id', async ({
    request,
  }) => {
    const res = await request.delete('/api/projects/nope');
    expect(res.status()).toBe(404);
  });

  test('GET /api/version reports the running version', async ({ request }) => {
    const res = await request.get('/api/version');
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('application/json');

    const body = await res.json();
    expect(typeof body.current).toBe('string');
    expect(body.current.length).toBeGreaterThan(0);
    expect(typeof body.latest).toBe('string');
    // The e2e daemon is a source build (`go run`) without a release channel:
    // it never updates itself, and that is not a failed release check.
    expect(body.channel).toBe('');
    expect(body.updateAvailable).toBe(false);
    expect(body.latest).toBe(body.current);
    expect(body.error).toBeUndefined();
  });

  test('rejects non-POST methods on /api/update', async ({ request }) => {
    const res = await request.get('/api/update');
    expect(res.status()).toBe(405);
  });

  test('POST /api/update refuses a build without a release channel', async ({
    request,
  }) => {
    // Only channel-stamped release builds replace themselves; the daemon must
    // say why (and leave itself running) rather than restart or hang.
    const res = await request.post('/api/update');
    expect(res.status()).toBe(503);
    const body = await res.json();
    expect(body.error).toContain('self-update is not supported');
  });

  test('answers on / with the web UI or the not-embedded hint', async ({
    request,
  }) => {
    const res = await request.get('/');
    // A dev daemon (`go run`) may not have the web UI embedded yet; it must
    // still answer deterministically either way.
    expect([200, 404]).toContain(res.status());
    if (res.status() === 404) {
      expect(await res.text()).toContain('make build');
    }
  });

  test('every /api response carries a monotonic X-Request-Id', async ({
    request,
  }) => {
    const first = await request.get('/api/hello');
    const second = await request.get('/api/hello');

    const id1 = Number(first.headers()['x-request-id']);
    const id2 = Number(second.headers()['x-request-id']);

    expect(Number.isInteger(id1)).toBe(true);
    expect(id1).toBeGreaterThan(0);
    expect(Number.isInteger(id2)).toBe(true);
    expect(id2).toBeGreaterThan(id1);
  });
});

interface SseEvent {
  type: string;
  data: string;
  id?: string;
}

/** Reads a text/event-stream until `count` events arrived. */
async function readSseEvents(
  body: ReadableStream<Uint8Array>,
  count: number,
  timeoutMs: number,
): Promise<SseEvent[]> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  const deadline = Date.now() + timeoutMs;
  const events: SseEvent[] = [];
  let buffer = '';
  try {
    while (events.length < count) {
      if (Date.now() > deadline) {
        throw new Error(`timed out after ${events.length} SSE events`);
      }
      const { value, done } = await reader.read();
      if (done) {
        throw new Error(`stream ended after ${events.length} SSE events`);
      }
      buffer += decoder.decode(value, { stream: true });
      let separator: number;
      while ((separator = buffer.indexOf('\n\n')) >= 0) {
        const block = buffer.slice(0, separator);
        buffer = buffer.slice(separator + 2);
        const event: SseEvent = { type: 'message', data: '' };
        for (const line of block.split('\n')) {
          if (line.startsWith('event: ')) event.type = line.slice(7);
          else if (line.startsWith('data: ')) event.data += line.slice(6);
          else if (line.startsWith('id: ')) event.id = line.slice(4);
        }
        events.push(event);
      }
    }
  } finally {
    await reader.cancel().catch(() => undefined);
  }
  return events;
}
