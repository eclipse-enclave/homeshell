import { type APIRequestContext } from '@playwright/test';
import { expect, test } from '../fixtures';
import { createProject } from './workstream-helpers';

/**
 * Workstream names are unique per project.
 *
 * A name is what a user picks a workstream out by — the card header, the delete
 * confirmation, the terminal title — so two wearing it makes all of those
 * ambiguous. The daemon refuses the second with a 409 on **both** doors: the
 * create and the rename. It is refused before anything is materialized, so a
 * rejected name never leaves a workstream behind to clean up.
 */

async function create(
  request: APIRequestContext,
  projectId: string,
  name: string,
  branch: string,
) {
  return request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name, config: { branch } },
  });
}

/** Polls until the real (non-placeholder) workstream with this name appears. */
async function awaitNamed(
  request: APIRequestContext,
  projectId: string,
  name: string,
): Promise<string> {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const found = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find(
        (w: { id: string; name: string }) =>
          w.name === name && !w.id.startsWith('creating-'),
      );
    if (found) return found.id as string;
    await new Promise((r) => setTimeout(r, 40));
  }
  throw new Error(`timed out waiting for a workstream named ${name}`);
}

async function workstreamsOf(request: APIRequestContext, projectId: string) {
  const state = await (await request.get('/api/projects')).json();
  return (
    state.projects?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams ?? []
  );
}

test('a second workstream cannot take a name already in the project', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-ws-name-');
  expect((await create(request, projectId, 'feature', 'main')).status()).toBe(
    201,
  );
  await awaitNamed(request, projectId, 'feature');

  const dup = await create(request, projectId, 'feature', 'other');
  expect(dup.status()).toBe(409);
  const body = await dup.json();
  expect(body.error).toContain('feature');
  // Said on the name, so a form can show it under the input to change.
  expect(body.fields).toEqual({
    '/name': '"feature" is already used in this project; enter another',
  });

  // Refused before anything was materialized: no placeholder, no directory, no
  // failed card to dispose of.
  expect(await workstreamsOf(request, projectId)).toHaveLength(1);
});

test('names differing only by case or padding are the same name', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-ws-name-');
  expect((await create(request, projectId, 'Feature', 'main')).status()).toBe(
    201,
  );
  await awaitNamed(request, projectId, 'Feature');

  for (const name of ['feature', 'FEATURE', '  Feature  ']) {
    const res = await create(request, projectId, name, 'other');
    expect(res.status(), `creating ${JSON.stringify(name)}`).toBe(409);
  }
  // A genuinely different name still works.
  expect((await create(request, projectId, 'feature-2', 'other')).status()).toBe(
    201,
  );
});

test('the same name in another project is fine', async ({ request }) => {
  const first = await createProject(request, 'hs-ws-name-');
  const second = await createProject(request, 'hs-ws-name-');

  expect((await create(request, first, 'main', 'main')).status()).toBe(201);
  await awaitNamed(request, first, 'main');
  expect((await create(request, second, 'main', 'main')).status()).toBe(201);
});

test('a rename cannot take another workstream’s name', async ({ request }) => {
  const projectId = await createProject(request, 'hs-ws-name-');
  expect((await create(request, projectId, 'feature', 'main')).status()).toBe(
    201,
  );
  await awaitNamed(request, projectId, 'feature');
  expect((await create(request, projectId, 'other', 'second')).status()).toBe(
    201,
  );
  const otherId = await awaitNamed(request, projectId, 'other');

  const refused = await request.patch(
    `/api/projects/${projectId}/workstreams/${otherId}`,
    { data: { name: 'Feature' } },
  );
  expect(refused.status()).toBe(409);
  // Said on the name as a create's refusal is, quoting the name already there.
  expect((await refused.json()).fields).toEqual({
    '/name': '"feature" is already used in this project; enter another',
  });

  // Nothing changed.
  const names = (await workstreamsOf(request, projectId)).map(
    (w: { name: string }) => w.name,
  );
  expect(names.sort()).toEqual(['feature', 'other']);
});

test('a rename to a free name still works, and to its own name is a no-op', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-ws-name-');
  expect((await create(request, projectId, 'feature', 'main')).status()).toBe(
    201,
  );
  const id = await awaitNamed(request, projectId, 'feature');

  // Its own name is not a conflict with itself.
  const same = await request.patch(
    `/api/projects/${projectId}/workstreams/${id}`,
    { data: { name: 'feature' } },
  );
  expect(same.status()).toBe(204);

  const renamed = await request.patch(
    `/api/projects/${projectId}/workstreams/${id}`,
    { data: { name: 'renamed' } },
  );
  expect(renamed.status()).toBe(204);
  await awaitNamed(request, projectId, 'renamed');

  // …and the freed name can be used again.
  expect((await create(request, projectId, 'feature', 'second')).status()).toBe(
    201,
  );
});
