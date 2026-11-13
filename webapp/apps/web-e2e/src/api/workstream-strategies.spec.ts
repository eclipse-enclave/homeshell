import { expect, tempProjectDir, test } from '../fixtures';
import { tempGitRepo } from '../git-repo';
import { addEmptyProject, addProjectWithDefault } from '../projects';
import { awaitWorkstreamNamed, coreStrategy } from './workstream-helpers';

/**
 * The create form offers a strategy only while it can be used: once the
 * project folder is taken, in place is not offered.
 */

test('in place is offered, and outside git preselected, only while the project folder is free', async ({
  request,
}) => {
  const dir = await tempProjectDir('hs-strategies-');
  const projectId = await addEmptyProject(request, dir);

  expect(await coreStrategy(request, projectId)).toMatchObject({
    enum: ['in-place', 'copy'],
    default: 'in-place',
  });

  const added = await request.post(
    `/api/projects/${projectId}/default-workstream`,
  );
  expect(added.status()).toBe(201);

  expect(await coreStrategy(request, projectId)).toMatchObject({
    enum: ['copy'],
    enumNames: ['Folder: a copy of the project'],
    default: 'copy',
  });
});

test('a copy shows as a folder, and keeps its stored strategy', async ({
  request,
}) => {
  const dir = await tempGitRepo('hs-strategies-label-');
  const { projectId } = await addProjectWithDefault(request, dir);
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'core', name: 'copied', config: { strategy: 'copy' } },
  });
  expect(res.status()).toBe(201);
  expect(await awaitWorkstreamNamed(request, projectId, 'copied')).toMatchObject(
    { strategyId: 'copy', strategy: 'folder' },
  );
});
