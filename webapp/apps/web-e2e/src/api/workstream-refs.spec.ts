import { basename } from 'node:path';
import { type APIRequestContext } from '@playwright/test';
import { expect, test } from '../fixtures';
import { git, gitProject } from '../git-repo';
import { awaitRealWorkstream } from './workstream-helpers';

/**
 * What the core provider's creation schema knows about a git-backed project's
 * refs: the base a new branch starts from, prefilled, and the refs to pick
 * from. The project is checked out on a feature branch, so the default base
 * has to be the repository's default branch rather than whatever is current.
 */

interface SchemaProperty {
  default?: string;
  examples: string[];
  format?: string;
}

async function coreSchema(request: APIRequestContext, projectId: string) {
  const res = await request.get(
    `/api/projects/${projectId}/workstream-providers`,
  );
  expect(res.status()).toBe(200);
  const providers: {
    id: string;
    workstreamSchema: { properties: Record<string, SchemaProperty> };
  }[] = await res.json();
  const core = providers.find((provider) => provider.id === 'core');
  if (!core) throw new Error('the core provider is not offered');
  return core.workstreamSchema;
}

async function createWorktree(
  request: APIRequestContext,
  projectId: string,
  config: Record<string, string | undefined>,
  name?: string,
) {
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      name,
      config: { strategy: 'worktree', ...config },
    },
  });
  expect(res.status()).toBe(201);
  return awaitRealWorkstream(request, projectId, 20_000);
}

test('the schema prefills the default base and suggests the refs', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-refs-');
  const { branch, baseRef } = (await coreSchema(request, projectId)).properties;

  expect(baseRef.default).toBe('main');
  expect(baseRef.examples).toEqual(
    expect.arrayContaining(['main', 'feature', 'v1.0']),
  );
  // Only local branches can be reused as the workstream's branch, and not the
  // checked-out feature: git refuses a second worktree on it.
  expect(branch.examples).toEqual(['main']);
});

test('a create with the prefilled base starts the branch there', async ({
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-refs-');
  const { baseRef } = (await coreSchema(request, projectId)).properties;

  const workstream = await createWorktree(request, projectId, {
    branch: 'fix-login-bug',
    baseRef: baseRef.default,
  });
  expect(workstream.branch).toBe('fix-login-bug');
  expect(await git(dir, 'rev-parse', 'fix-login-bug')).toBe(
    await git(dir, 'rev-parse', 'main'),
  );
});

// With a tag called main too, a bare "main" is ambiguous and git refuses to
// start a branch from it.
test('a branch that shares its name with a tag is suggested unambiguously', async ({
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-refs-');
  await git(dir, 'tag', 'main', 'main');
  const { branch, baseRef } = (await coreSchema(request, projectId)).properties;

  expect(baseRef.default).toBe('heads/main');
  expect(baseRef.examples).toEqual(
    expect.arrayContaining(['heads/main', 'tags/main']),
  );
  expect(branch.examples).toEqual(['main']);

  await createWorktree(request, projectId, {
    branch: 'from-heads-main',
    baseRef: baseRef.default,
  });
  expect(await git(dir, 'rev-parse', 'from-heads-main')).toBe(
    await git(dir, 'rev-parse', 'heads/main'),
  );
});

// The daemon derives a branch left empty from the workstream's final name, so
// a create that leaves the name to the daemon too is not refused.
test('a worktree left without a branch is on one named after the workstream', async ({
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-refs-');

  const named = await createWorktree(
    request,
    projectId,
    { branch: '' },
    'Fix Login Bug',
  );
  expect(named.branch).toBe('fix-login-bug');
  expect(
    await git(dir, 'rev-parse', '--verify', 'refs/heads/fix-login-bug'),
  ).not.toBe('');
});

test('a worktree left without a name or a branch is on one named after the project', async ({
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-refs-');

  const workstream = await createWorktree(request, projectId, { branch: '' });
  expect(workstream.name).toBe(basename(dir));
  expect(workstream.branch).toBe(basename(dir).toLowerCase());
  expect(
    await git(dir, 'rev-parse', '--verify', `refs/heads/${workstream.branch}`),
  ).not.toBe('');
});

test('a create without a base starts from the default branch, not the current one', async ({
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-refs-');

  await createWorktree(request, projectId, { branch: 'no-base' });
  expect(await git(dir, 'rev-parse', 'no-base')).toBe(
    await git(dir, 'rev-parse', 'main'),
  );
});

// The form sends the branch it derived from the name whatever the strategy; a
// workstream that is not a worktree is on no branch of its own. In place, it is
// on the branch checked out in the project; a copy is on none.
test('a non-worktree workstream carries no branch of its own, whatever was sent', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-refs-');

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      config: {
        strategy: 'in-place',
        branch: 'fix-login-bug',
        baseRef: 'main',
      },
    },
  });
  expect(res.status()).toBe(201);
  const workstream = await awaitRealWorkstream(request, projectId, 20_000);
  expect(workstream.branch).toBe('feature');
});
