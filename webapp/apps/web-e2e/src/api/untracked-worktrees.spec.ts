import { access } from 'node:fs/promises';
import type { APIRequestContext } from '@playwright/test';
import { expect, test, tempProjectDir } from '../fixtures';
import { addWorktree, git, gitProject, tempGitRepo } from '../git-repo';
import { addProjectWithDefault } from '../projects';
import {
  addUntrackedWorktree,
  awaitRealWorkstream,
  createProject,
  createSession,
  createWorkstream,
  listedProjects,
  pollFor,
  projectOf,
  sessionsOf,
  untrackedOf,
  workstreamOf,
  type RealWorkstream,
} from './workstream-helpers';

/**
 * Black-box tests for untracked worktrees: the worktrees of a project's
 * repository that HomeShell did not make, which it suggests adding as
 * workstreams. The daemon refreshes the suggestions on its refresh interval,
 * so every read of them polls.
 */

/** Polls until the project suggests the worktree at `path`. */
const awaitSuggested = (
  request: APIRequestContext,
  projectId: string,
  path: string,
) =>
  pollFor(`${path} to be suggested`, async () =>
    (await untrackedOf(request, projectId)).find((w) => w.path === path),
  );

/**
 * A git project with one untracked worktree, made with `options`, once the
 * project suggests it.
 */
async function suggestedWorktree(
  request: APIRequestContext,
  options?: Parameters<typeof addWorktree>[1],
): Promise<{ dir: string; projectId: string; path: string }> {
  const { dir, projectId } = await gitProject(request, 'hs-untracked-');
  const path = await addWorktree(dir, options);
  await awaitSuggested(request, projectId, path);
  return { dir, projectId, path };
}

/** Adds the untracked worktree at `path`, expecting a 201, and returns the workstream. */
async function addedWorktree(
  request: APIRequestContext,
  projectId: string,
  path: string,
): Promise<RealWorkstream> {
  const res = await addUntrackedWorktree(request, projectId, path);
  expect(res.status(), await res.text()).toBe(201);
  return res.json();
}

/**
 * Waits until a refresh has run that started after now: a new worktree is
 * suggested only by one.
 */
async function awaitRefresh(
  request: APIRequestContext,
  projectId: string,
  repo: string,
): Promise<void> {
  await awaitSuggested(request, projectId, await addWorktree(repo));
}

/** Creates a worktree workstream through the core provider and returns it. */
async function createOwnWorktree(
  request: APIRequestContext,
  projectId: string,
  name: string,
): Promise<RealWorkstream> {
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'core', name, config: { strategy: 'worktree' } },
  });
  expect(res.status(), await res.text()).toBe(201);
  return awaitRealWorkstream(request, projectId, 20_000);
}

/** Every workstream of every project, as listed right now. */
async function allWorkstreams(
  request: APIRequestContext,
): Promise<RealWorkstream[]> {
  return (await listedProjects(request)).flatMap(
    (project) => project.workstreams ?? [],
  );
}

const exists = (path: string) =>
  access(path).then(
    () => true,
    () => false,
  );

test.describe('untracked worktrees API', () => {
  test('a worktree added outside HomeShell is suggested, named after its branch', async ({
    request,
  }) => {
    const { projectId, path } = await suggestedWorktree(request, {
      branch: 'fix-login',
    });

    const [suggested] = await untrackedOf(request, projectId);
    expect(suggested).toMatchObject({
      path,
      name: 'fix-login',
      branch: 'fix-login',
      sessionCount: 0,
    });
  });

  test('a detached worktree is suggested under its folder name', async ({
    request,
  }) => {
    const { projectId, path } = await suggestedWorktree(request, {
      detach: true,
    });

    const [suggested] = await untrackedOf(request, projectId);
    expect(suggested.name).toBe(path.split('/').pop());
    expect(suggested.branch).toBeUndefined();
  });

  test("the project folder and HomeShell's own worktrees are not suggested", async ({
    request,
  }) => {
    const { dir, projectId } = await gitProject(request, 'hs-untracked-');
    const own = await createOwnWorktree(request, projectId, 'own');
    // A refresh that has seen this one has seen the others.
    const outside = await addWorktree(dir);
    await awaitSuggested(request, projectId, outside);

    const paths = (await untrackedOf(request, projectId)).map((w) => w.path);
    expect(paths).toEqual([outside]);
    expect(paths).not.toContain(own.workingDir);
  });

  test('adding one makes a worktree workstream that follows its branch', async ({
    request,
  }) => {
    const { projectId, path } = await suggestedWorktree(request, {
      branch: 'topic',
    });

    const added = await addedWorktree(request, projectId, path);
    expect(added).toMatchObject({
      name: 'topic',
      strategyId: 'worktree',
      branch: 'topic',
      workingDir: path,
    });
    await pollFor('the suggestion to go', async () =>
      (await untrackedOf(request, projectId)).length === 0 ? true : undefined,
    );

    await git(path, 'checkout', '-q', '-b', 'other');
    const followed = await pollFor('the branch to follow', async () => {
      const ws = await workstreamOf(request, projectId, added.id);
      return ws?.branch === 'other' ? ws : undefined;
    });
    expect(followed.name).toBe('topic');
  });

  test('added under a name already taken, it is numbered', async ({
    request,
  }) => {
    const { projectId, path } = await suggestedWorktree(request, {
      branch: 'taken',
    });
    await createWorkstream(request, projectId, 'taken');

    const added = await addedWorktree(request, projectId, path);
    expect(added).toMatchObject({ name: 'taken-2', branch: 'taken' });
  });

  test('adding a path that is not an untracked worktree is refused', async ({
    request,
  }) => {
    const { dir, projectId, path } = await suggestedWorktree(request);

    expect((await addUntrackedWorktree(request, projectId, dir)).status()).toBe(
      409,
    );
    const elsewhere = await tempProjectDir('hs-untracked-elsewhere-');
    expect(
      (await addUntrackedWorktree(request, projectId, elsewhere)).status(),
    ).toBe(409);

    expect(
      (await addUntrackedWorktree(request, projectId, path)).status(),
    ).toBe(201);
    expect(
      (await addUntrackedWorktree(request, projectId, path)).status(),
    ).toBe(409);
  });

  test('a project outside git suggests nothing, and adds nothing', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-untracked-plain-');
    const elsewhere = await tempProjectDir('hs-untracked-elsewhere-');

    const res = await addUntrackedWorktree(request, projectId, elsewhere);
    expect(res.status()).toBe(409);
    expect(await untrackedOf(request, projectId)).toEqual([]);
  });

  test('adding needs a path, and a known project', async ({ request }) => {
    const { projectId } = await gitProject(request, 'hs-untracked-');
    for (const path of ['', '  ']) {
      const res = await addUntrackedWorktree(request, projectId, path);
      expect(res.status()).toBe(400);
    }
    const unknown = await addUntrackedWorktree(request, 'nope', '/tmp/x');
    expect(unknown.status()).toBe(404);
  });

  test('removed keeping its files, it is suggested again', async ({
    request,
  }) => {
    const { projectId, path } = await suggestedWorktree(request);
    const added = await addedWorktree(request, projectId, path);

    const removed = await request.delete(
      `/api/projects/${projectId}/workstreams/${added.id}`,
      { data: { keepFiles: true } },
    );
    expect(removed.status()).toBe(204);

    await awaitSuggested(request, projectId, path);
    expect(await exists(path)).toBe(true);
  });

  test('removed with its files, it is gone for good', async ({ request }) => {
    const { dir, projectId, path } = await suggestedWorktree(request);
    const added = await addedWorktree(request, projectId, path);

    const removed = await request.delete(
      `/api/projects/${projectId}/workstreams/${added.id}`,
    );
    expect(removed.status()).toBe(204);
    expect(await exists(path)).toBe(false);

    // A refresh that has seen this one has run since the removal.
    const sentinel = await addWorktree(dir);
    await awaitSuggested(request, projectId, sentinel);
    expect((await untrackedOf(request, projectId)).map((w) => w.path)).toEqual([
      sentinel,
    ]);
  });

  test('a worktree workstream disappears with its sessions when its worktree is removed outside', async ({
    request,
  }) => {
    const { dir, projectId } = await gitProject(request, 'hs-untracked-');
    // First: createOwnWorktree returns the project's first real workstream.
    const made = await createOwnWorktree(request, projectId, 'made');
    const path = await addWorktree(dir);
    await awaitSuggested(request, projectId, path);
    const added = await addedWorktree(request, projectId, path);

    const sessionIds: string[] = [];
    for (const ws of [made, added]) {
      sessionIds.push((await createSession(request, projectId, ws.id)).id);
      await git(dir, 'worktree', 'remove', '--force', String(ws.workingDir));
      await pollFor(`${ws.name} to go`, async () =>
        (await workstreamOf(request, projectId, ws.id)) ? undefined : true,
      );
    }

    // A refresh after the removals would have adopted them again.
    await awaitRefresh(request, projectId, dir);
    const listed = await allWorkstreams(request);
    expect(listed.map((ws) => ws.workingDir)).not.toContain(made.workingDir);
    expect(listed.map((ws) => ws.workingDir)).not.toContain(path);
    const listedSessionIds = listed.flatMap((ws) =>
      (ws.sessions ?? []).map((session) => session.id),
    );
    for (const sessionId of sessionIds) {
      expect(listedSessionIds).not.toContain(sessionId);
    }
  });

  test('sessions in an untracked worktree are counted, not adopted, and attach once it is added', async ({
    request,
  }) => {
    const dir = await tempGitRepo('hs-untracked-');
    const path = await addWorktree(dir, { inside: true });
    // The default workstream lets the project be added again as the daemon sets it up.
    const first = await addProjectWithDefault(request, dir);
    await awaitSuggested(request, first.projectId, path);
    const added = await addedWorktree(request, first.projectId, path);
    await createSession(request, first.projectId, added.id);

    // The session is left running; it is found again with the project.
    const removed = await request.delete(`/api/projects/${first.projectId}`);
    expect(removed.status()).toBe(204);
    const again = await addProjectWithDefault(request, dir);

    await pollFor('the session to be counted', async () =>
      (await untrackedOf(request, again.projectId)).find(
        (w) => w.path === path && w.sessionCount === 1,
      ),
    );
    await awaitRefresh(request, again.projectId, dir);
    const project = await projectOf(request, again.projectId);
    expect(project?.workstreams?.map((ws) => ws.workingDir)).not.toContain(
      path,
    );

    const readded = await addedWorktree(request, again.projectId, path);
    await pollFor('the session to attach', async () =>
      (await sessionsOf(request, again.projectId, readded.id)).length === 1
        ? true
        : undefined,
    );
  });
});
