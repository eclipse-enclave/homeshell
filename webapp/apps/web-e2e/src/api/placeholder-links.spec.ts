import { expect, test } from '../fixtures';
import { isPlaceholderSession } from '../placeholders';
import {
  awaitRealWorkstream,
  createProject,
  createWorkstream,
  sessionsOf,
} from './workstream-helpers';

/**
 * A create answers with the placeholder that stands for what is coming up, and
 * what lands names that placeholder in `placeholderId`, so the client that
 * asked can tell its row from any other that appears meanwhile.
 */

test('a created workstream names the placeholder it replaced', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-link-e2e-');
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'linked', config: { branch: 'main' } },
  });
  expect(res.status()).toBe(201);
  const placeholder = await res.json();

  const real = await awaitRealWorkstream(request, projectId);
  expect(real.id).not.toBe(placeholder.id);
  expect((real as { placeholderId?: string }).placeholderId).toBe(
    placeholder.id,
  );
});

test('each created session names the placeholder it replaced', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-link-e2e-');
  const workstreamId = await createWorkstream(request, projectId, 'linked');
  const placeholderIds: string[] = [];
  for (let i = 0; i < 2; i++) {
    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
      { data: { providerId: 'fake', optionId: 'claude', config: {} } },
    );
    expect(res.status()).toBe(202);
    placeholderIds.push((await res.json()).id);
  }

  await expect
    .poll(
      async () =>
        (await sessionsOf(request, projectId, workstreamId))
          .filter((session) => !isPlaceholderSession(session.id))
          .map((session) => session.placeholderId)
          .sort(),
      { timeout: 10_000 },
    )
    .toEqual([...placeholderIds].sort());
});
