import { type APIRequestContext } from '@playwright/test';

import { expect, test } from '../fixtures';
import {
  awaitRealWorkstream,
  createProject,
  type RealWorkstream,
  workstreamOf,
} from './workstream-helpers';

/**
 * Creates a workstream and returns a reader for its current projection. The
 * label is derived by the projection that follows the create, so callers poll.
 */
async function createAndWatch(
  request: APIRequestContext,
  providerId: string,
  config: Record<string, string>,
): Promise<() => Promise<RealWorkstream | undefined>> {
  const projectId = await createProject(request, 'hs-label-e2e-');
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId, config },
  });
  expect(res.status()).toBe(201);
  const { id } = await awaitRealWorkstream(request, projectId);
  return () => workstreamOf(request, projectId, id);
}

test('labels a workstream with its provider display name', async ({
  request,
}) => {
  const read = await createAndWatch(request, 'fake', { branch: 'main' });
  await expect
    .poll(async () => (await read())?.providerLabel)
    .toBe('Fake Provider');
});

test('gives a workstream of the built-in provider no label', async ({
  request,
}) => {
  const read = await createAndWatch(request, 'core', { strategy: 'in-place' });
  // Its open modes show a projection has run, so the missing label is not
  // merely early.
  await expect
    .poll(async () => (await read())?.openModes.length)
    .toBeGreaterThan(0);
  expect((await read())?.providerLabel).toBeUndefined();
});
