import { execFile } from 'node:child_process';
import { writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { type APIRequestContext } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';

const execFileAsync = promisify(execFile);

/**
 * DELETE …/workstreams/{wsId}/status — discard a workstream's transient status
 * message.
 *
 * The message is how the daemon explains something transient about a workstream,
 * and nothing overwrites it until the next create, so it would otherwise stay
 * attached indefinitely. Dismissing is a daemon mutation rather than a
 * client-side hide because the message is daemon state: it broadcasts, so every
 * client drops it — except on a workstream whose own creation failed, where the
 * message is the failure's only account of itself and the clear is refused.
 */

async function createProject(request: APIRequestContext): Promise<string> {
  const dir = await tempProjectDir('hs-status-e2e-');
  const res = await request.post('/api/projects', { data: { path: dir } });
  expect(res.status()).toBe(201);
  return (await res.json()).id as string;
}

async function workstreamOf(request: APIRequestContext, projectId: string) {
  const state = await (await request.get('/api/projects')).json();
  return state.projects
    ?.find((p: { id: string }) => p.id === projectId)
    ?.workstreams?.find((w: { id: string }) => !w.id.startsWith('creating-'));
}

async function createWorkstream(
  request: APIRequestContext,
  projectId: string,
): Promise<string> {
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'status', config: { branch: 'main' } },
  });
  expect(res.status()).toBe(201);
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    const ws = await workstreamOf(request, projectId);
    if (ws) return ws.id as string;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error('timed out waiting for the workstream');
}

/*
 * A *failed session create* used to be the way to produce a dismissible message
 * here; it now stays on the session it belongs to (api/session-error.spec.ts).
 * The workstream keeps the message only as the fallback for a create whose
 * placeholder had already gone — which nothing over HTTP can arrange — so the
 * clear-a-real-message path is unit-tested instead
 * (daemon/internal/api/workstreams_status_test.go). What is still drivable end to
 * end is below: the idempotent clear, the unknown target, and the refusal on a
 * workstream whose own creation failed.
 */

test('dismissing twice is not an error', async ({ request }) => {
  const projectId = await createProject(request);
  const workstreamId = await createWorkstream(request, projectId);

  for (let i = 0; i < 2; i++) {
    const res = await request.delete(
      `/api/projects/${projectId}/workstreams/${workstreamId}/status`,
    );
    expect(res.status()).toBe(204);
  }
});

test('dismissing on an unknown workstream is a 404 with a JSON error', async ({
  request,
}) => {
  const projectId = await createProject(request);

  const res = await request.delete(
    `/api/projects/${projectId}/workstreams/ws-nope/status`,
  );
  expect(res.status()).toBe(404);
  expect((await res.json()).error).toBeTruthy();
});

/**
 * A git repository with one commit on `main`, which `main` is checked out in — so
 * asking the core provider for a worktree on `main` is a creation that genuinely
 * fails, rather than one faked into failing.
 */
async function gitProject(request: APIRequestContext): Promise<string> {
  const dir = await tempProjectDir('hs-status-err-e2e-');
  const env = {
    ...process.env,
    GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_CONFIG_SYSTEM: '/dev/null',
    GIT_AUTHOR_NAME: 'e2e',
    GIT_AUTHOR_EMAIL: 'e2e@example.com',
    GIT_COMMITTER_NAME: 'e2e',
    GIT_COMMITTER_EMAIL: 'e2e@example.com',
  };
  await execFileAsync('git', ['init', '-q', '-b', 'main'], { cwd: dir, env });
  await writeFile(join(dir, 'README.md'), '# e2e\n');
  await execFileAsync('git', ['add', '.'], { cwd: dir, env });
  await execFileAsync('git', ['commit', '-qm', 'initial'], { cwd: dir, env });

  const res = await request.post('/api/projects', { data: { path: dir } });
  expect(res.status()).toBe(201);
  return (await res.json()).id as string;
}

/** Polls for the placeholder left behind by a failed creation. */
async function awaitFailedWorkstream(
  request: APIRequestContext,
  projectId: string,
): Promise<{ id: string; statusMessage: string; createFailed?: boolean }> {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const ws = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find(
        (w: { runtimeState: string }) => w.runtimeState === 'error',
      );
    if (ws?.statusMessage) return ws;
    await new Promise((r) => setTimeout(r, 40));
  }
  throw new Error('timed out waiting for the failed workstream');
}

// The regression this exists for: a failed creation's message is the whole of
// what the card says, and dismissing it left a permanently red workstream that
// explained nothing. Removing it is the disposal — blanking it is not.
test('the message on a failed workstream cannot be dismissed', async ({
  request,
}) => {
  const projectId = await gitProject(request);

  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      name: 'dup-branch',
      // git keeps branches as files: `main` is one where `main/topic` would need
      // a folder, which only `git worktree add` finds out.
      config: { strategy: 'worktree', branch: 'main/topic' },
    },
  });
  expect(created.status()).toBe(201);

  const failed = await awaitFailedWorkstream(request, projectId);
  expect(failed.statusMessage).toContain('main');

  const dismissed = await request.delete(
    `/api/projects/${projectId}/workstreams/${failed.id}/status`,
  );
  expect(dismissed.status()).toBe(409);
  expect((await dismissed.json()).error).toBeTruthy();

  // The refusal is a no-op: the explanation the user tried to clear survives.
  const after = await awaitFailedWorkstream(request, projectId);
  expect(after.statusMessage).toBe(failed.statusMessage);

  // …and the way out is still open. A failed placeholder deletes unconditionally.
  const removed = await request.delete(
    `/api/projects/${projectId}/workstreams/${failed.id}`,
  );
  expect(removed.status()).toBe(204);
});

// A failed create is marked as one, so a client offers nothing on it but its
// removal, and it says what went wrong in git's words, without its exit status.
test('a failed create is marked and says why in words', async ({ request }) => {
  const projectId = await gitProject(request);

  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      name: 'df-conflict',
      config: { strategy: 'worktree', branch: 'main/topic' },
    },
  });
  expect(created.status()).toBe(201);

  const failed = await awaitFailedWorkstream(request, projectId);
  expect(failed.createFailed).toBe(true);
  expect(failed.statusMessage).toMatch(
    /^could not create the worktree: cannot lock ref 'refs\/heads\/main\/topic'/,
  );
  expect(failed.statusMessage).not.toMatch(/fatal:|exit status/);
});
