import { basename } from 'node:path';
import { expect, tempProjectDir, test } from '../fixtures';
import { git, tempGitRepo } from '../git-repo';
import { isPlaceholderWorkstream } from '../placeholders';
import { addEmptyProject, addProjectWithDefault } from '../projects';
import {
  awaitWorkstreamNamed,
  coreStrategy,
  createSession,
  pollFor,
  workstreamOf,
} from './workstream-helpers';

/**
 * A project comes with a workstream to start in: in place, in the project
 * folder, named after the branch checked out there. Creating another one
 * defaults to a git worktree where the project is a repository.
 */

test('a git project comes with an in-place workstream named after its branch', async ({
  request,
}) => {
  const dir = await tempGitRepo('hs-default-ws-');
  const { workstream } = await addProjectWithDefault(request, dir);
  expect(workstream).toMatchObject({
    name: 'feature',
    branch: 'feature',
    strategyId: 'in-place',
    provider: 'core',
    inProjectFolder: true,
  });
  expect(basename(workstream.workingDir)).toBe(basename(dir));
});

test('a project outside git comes with one named after its folder', async ({
  request,
}) => {
  const dir = await tempProjectDir('hs-default-ws-plain-');
  const { workstream } = await addProjectWithDefault(request, dir);
  expect(workstream.name).toBe(basename(dir));
  expect(workstream.branch ?? '').toBe('');
  expect(workstream.strategyId).toBe('in-place');
});

test('a session can be started in it straight away', async ({ request }) => {
  const dir = await tempProjectDir('hs-default-ws-session-');
  const { projectId, workstream } = await addProjectWithDefault(request, dir);
  await createSession(request, projectId, workstream.id);
});

test('its name follows a branch switched outside HomeShell', async ({
  request,
}) => {
  const dir = await tempGitRepo('hs-default-ws-switch-');
  const { projectId, workstream } = await addProjectWithDefault(request, dir);

  await git(dir, 'checkout', '-q', 'main');
  await pollFor('the workstream to follow the switch to main', async () => {
    const current = await workstreamOf(request, projectId, workstream.id);
    return current?.name === 'main' && current.branch === 'main'
      ? current
      : undefined;
  });
});

test('a renamed one keeps its name while its branch follows', async ({
  request,
}) => {
  const dir = await tempGitRepo('hs-default-ws-renamed-');
  const { projectId, workstream } = await addProjectWithDefault(request, dir);
  const renamed = await request.patch(
    `/api/projects/${projectId}/workstreams/${workstream.id}`,
    { data: { name: 'my work' } },
  );
  expect(renamed.status()).toBe(204);

  await git(dir, 'checkout', '-q', 'main');
  const current = await pollFor('the branch to follow the switch', async () => {
    const listed = await workstreamOf(request, projectId, workstream.id);
    return listed?.branch === 'main' ? listed : undefined;
  });
  expect(current.name).toBe('my work');
});

test('a worktree follows a branch switched in its folder', async ({
  request,
}) => {
  const dir = await tempGitRepo('hs-default-ws-worktree-switch-');
  const { projectId } = await addProjectWithDefault(request, dir);
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      name: 'topic',
      config: { strategy: 'worktree' },
    },
  });
  expect(res.status()).toBe(201);
  const { id, workingDir } = await awaitWorkstreamNamed(
    request,
    projectId,
    'topic',
  );
  if (!workingDir) throw new Error('the worktree lists no folder');

  await git(workingDir, 'checkout', '-q', '-b', 'topic-2');
  const current = await pollFor('the branch to follow the switch', async () => {
    const listed = await workstreamOf(request, projectId, id);
    return listed?.branch === 'topic-2' ? listed : undefined;
  });
  expect(current.name).toBe('topic');
});

test('a new workstream in a git project defaults to a worktree', async ({
  request,
}) => {
  const dir = await tempGitRepo('hs-default-ws-schema-');
  const { projectId } = await addProjectWithDefault(request, dir);

  expect((await coreStrategy(request, projectId)).default).toBe('worktree');

  // A create naming no strategy gets the same default.
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'core', name: 'topic', config: {} },
  });
  expect(res.status()).toBe(201);
  const created = await awaitWorkstreamNamed(request, projectId, 'topic');
  expect(created.strategyId).toBe('worktree');
  expect(created.branch).toBe('topic');
  expect(created.inProjectFolder).toBeUndefined();
});

test('an in-place create takes the project folder before it is made', async ({
  request,
}) => {
  const dir = await tempProjectDir('hs-default-ws-placeholder-');
  const projectId = await addEmptyProject(request, dir);
  const create = () =>
    request.post(`/api/projects/${projectId}/workstreams`, {
      data: { providerId: 'core', name: '', config: { strategy: 'in-place' } },
    });
  const res = await create();
  expect(res.status()).toBe(201);
  const placeholder = await res.json();
  expect(isPlaceholderWorkstream(placeholder.id)).toBe(true);
  expect(placeholder.inProjectFolder).toBe(true);

  expect((await create()).status()).toBe(400);
  expect(
    (
      await request.post(`/api/projects/${projectId}/default-workstream`)
    ).status(),
  ).toBe(409);
});

test('a project without one can be given its default workstream again', async ({
  request,
}) => {
  const dir = await tempGitRepo('hs-default-ws-again-');
  const { projectId, workstream } = await addProjectWithDefault(request, dir);
  const url = `/api/projects/${projectId}/default-workstream`;

  const taken = await request.post(url);
  expect(taken.status()).toBe(409);

  await request.delete(
    `/api/projects/${projectId}/workstreams/${workstream.id}`,
  );
  const res = await request.post(url);
  expect(res.status()).toBe(201);
  expect(await res.json()).toMatchObject({
    name: 'feature',
    strategyId: 'in-place',
    inProjectFolder: true,
  });
});
