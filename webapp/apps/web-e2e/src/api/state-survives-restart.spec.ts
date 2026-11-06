import { expect, test } from '@playwright/test';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { buildDaemon, isolatedDaemon, type IsolatedDaemon } from '../isolated-daemon';

/**
 * What a daemon persists, the next daemon in the same data directory serves
 * again: the projects, their names, and their workstreams. Black-box, through
 * the public API, a SIGKILL and `homeshell stop`, so it holds whatever the
 * daemon writes its state to.
 */

interface Workstream {
  id: string;
  name: string;
  provider: string;
  workingDir?: string;
}

interface Project {
  id: string;
  name: string;
  path: string;
  workstreams: Workstream[];
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function projects(daemon: IsolatedDaemon): Promise<Project[]> {
  const res = await daemon.api('/api/projects');
  expect(res.status).toBe(200);
  return ((await res.json()) as { projects: Project[] }).projects;
}

/** Polls until the project lists a workstream that is not a placeholder. */
async function realWorkstream(
  daemon: IsolatedDaemon,
  projectId: string,
): Promise<Workstream> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const project = (await projects(daemon)).find((p) => p.id === projectId);
    const real = project?.workstreams.find(
      (w) => !w.id.startsWith('creating-'),
    );
    if (real) return real;
    await sleep(50);
  }
  throw new Error('timed out waiting for the real workstream');
}

test.describe('persisted state survives a restart', () => {
  let daemon: IsolatedDaemon;

  test.beforeAll(() => {
    test.setTimeout(120_000);
    daemon = isolatedDaemon(buildDaemon('state-restart'), 'state-restart');
  });

  test.afterAll(() => daemon.dispose());

  test('a project, its name and its workstream are served again after a restart', async () => {
    test.setTimeout(90_000);
    await daemon.start();

    const path = mkdtempSync(join(tmpdir(), 'homeshell-state-project-'));
    const created = await daemon.api('/api/projects', {
      method: 'POST',
      body: JSON.stringify({ path }),
    });
    expect(created.status).toBe(201);
    const projectId = ((await created.json()) as Project).id;

    const renamed = await daemon.api(`/api/projects/${projectId}`, {
      method: 'PATCH',
      body: JSON.stringify({ name: 'renamed before the restart' }),
    });
    expect(renamed.status).toBe(204);

    const createdWorkstream = await daemon.api(
      `/api/projects/${projectId}/workstreams`,
      {
        method: 'POST',
        body: JSON.stringify({
          providerId: 'core',
          name: 'in place',
          config: { strategy: 'in-place' },
        }),
      },
    );
    expect(createdWorkstream.status).toBe(201);
    const workstream = await realWorkstream(daemon, projectId);
    expect(workstream.name).toBe('in place');

    // Killed rather than stopped: the state has to be written before the API
    // answers, not only on a clean shutdown.
    await daemon.kill();
    await daemon.start();

    const after = await projects(daemon);
    expect(after).toHaveLength(1);
    expect(after[0].id).toBe(projectId);
    expect(after[0].name).toBe('renamed before the restart');
    expect(after[0].path).toBe(path);
    const restored = after[0].workstreams.find((w) => w.id === workstream.id);
    expect(restored).toBeDefined();
    expect(restored?.name).toBe('in place');
    expect(restored?.provider).toBe('core');

    await daemon.stop();
  });
});
