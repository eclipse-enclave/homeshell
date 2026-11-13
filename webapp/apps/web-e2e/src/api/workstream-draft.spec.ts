import { rm } from 'node:fs/promises';
import { basename, dirname, join } from 'node:path';
import { expect, test } from '../fixtures';
import { gitProject } from '../git-repo';
import { withSettingsLock } from '../settings-lock';
import { awaitRealWorkstream, createProject } from './workstream-helpers';

/**
 * A draft answers what a create would derive for what it leaves blank — the
 * name, a worktree's branch, a managed directory — so a form can show it
 * before it is sent, and creates nothing.
 */

interface Draft {
  name: string;
  config: Record<string, string>;
}

test('a blank in-place draft takes the branch checked out in the project', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-draft-');
  const res = await request.post(
    `/api/projects/${projectId}/workstream-draft`,
    {
      data: { providerId: 'core', config: { strategy: 'in-place' } },
    },
  );
  expect(res.status()).toBe(200);
  expect(await res.json()).toEqual({
    name: 'feature',
    config: { branch: 'feature', path: '' },
  });

  const state = await (await request.get('/api/projects')).json();
  const project = state.projects.find(
    (p: { id: string }) => p.id === projectId,
  );
  expect(project.workstreams).toHaveLength(0);
});

test('a blank in-place draft outside git takes the project folder name', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-draft-plain-');
  const res = await request.post(
    `/api/projects/${projectId}/workstream-draft`,
    {
      data: { providerId: 'core', config: { strategy: 'in-place' } },
    },
  );
  expect(res.status()).toBe(200);
  const draft: Draft = await res.json();
  expect(draft.name).toMatch(/^hs-draft-plain-/);
  expect(draft.config).toEqual({ branch: '', path: '' });
});

// Tests that draft and then create hold the settings lock: both read
// workstreams.location, which other tests set.
test('a worktree is created as its draft said', async ({ request }) => {
  await withSettingsLock(async () => {
    const { projectId } = await gitProject(request, 'hs-draft-');
    const input = {
      providerId: 'core',
      name: 'Fix Login',
      config: { strategy: 'worktree' },
    };
    const draft: Draft = await (
      await request.post(`/api/projects/${projectId}/workstream-draft`, {
        data: input,
      })
    ).json();
    expect(draft.name).toBe('Fix Login');
    expect(draft.config.branch).toBe('fix-login');
    expect(draft.config.path).toMatch(/\/hs-draft-[^/]*-fix-login$/);

    const res = await request.post(`/api/projects/${projectId}/workstreams`, {
      data: input,
    });
    expect(res.status()).toBe(201);
    const created = await awaitRealWorkstream(request, projectId, 20_000);
    expect(created.branch).toBe(draft.config.branch);
    expect(created.workingDir).toBe(draft.config.path);
  });
});

test('a relative workstreams location is taken from the project', async ({
  request,
}) => {
  await withSettingsLock(async () => {
    const { dir, projectId } = await gitProject(request, 'hs-draft-');
    const current = await (await request.get('/api/settings')).json();
    const put = await request.put('/api/settings', {
      data: {
        ...current,
        workstreams: { ...current.workstreams, location: '../hs-relative' },
      },
    });
    expect(put.status()).toBe(200);
    try {
      const draft: Draft = await (
        await request.post(`/api/projects/${projectId}/workstream-draft`, {
          data: { providerId: 'core', name: 'Rel', config: { strategy: 'copy' } },
        })
      ).json();
      expect(dirname(draft.config.path)).toBe(join(dirname(dir), 'hs-relative'));
    } finally {
      await request.put('/api/settings', { data: current });
    }
  });
});

test('a copy draft proposes no path inside the project, and create says why', async ({
  request,
}) => {
  await withSettingsLock(async () => {
    const { projectId } = await gitProject(request, 'hs-draft-');
    const current = await (await request.get('/api/settings')).json();
    const put = await request.put('/api/settings', {
      data: {
        ...current,
        workstreams: { ...current.workstreams, location: 'copies' },
      },
    });
    expect(put.status()).toBe(200);
    try {
      const input = {
        providerId: 'core',
        name: 'Inside',
        config: { strategy: 'copy' },
      };
      const draft: Draft = await (
        await request.post(`/api/projects/${projectId}/workstream-draft`, {
          data: input,
        })
      ).json();
      expect(draft.config.path).toBe('');

      const res = await request.post(`/api/projects/${projectId}/workstreams`, {
        data: input,
      });
      expect(res.status()).toBe(400);
      expect((await res.json()).fields['/path']).toContain(
        'workstreams location setting',
      );
    } finally {
      await request.put('/api/settings', { data: current });
    }
  });
});

test('a very long name gets a directory within filesystem limits', async ({
  request,
}) => {
  await withSettingsLock(async () => {
    const { projectId } = await gitProject(request, 'hs-draft-');
    const input = {
      providerId: 'core',
      name: 'word '.repeat(60).trim(),
      config: { strategy: 'copy' },
    };
    const draft: Draft = await (
      await request.post(`/api/projects/${projectId}/workstream-draft`, {
        data: input,
      })
    ).json();
    expect(basename(draft.config.path).length).toBeLessThanOrEqual(64);

    const res = await request.post(`/api/projects/${projectId}/workstreams`, {
      data: input,
    });
    expect(res.status()).toBe(201);
    const created = await awaitRealWorkstream(request, projectId, 20_000);
    expect(created.workingDir).toBe(draft.config.path);
  });
});

test('a very long name gets a worktree branch within git limits', async ({
  request,
}) => {
  await withSettingsLock(async () => {
    const { projectId } = await gitProject(request, 'hs-draft-');
    const input = {
      providerId: 'core',
      name: 'x'.repeat(300),
      config: { strategy: 'worktree' },
    };
    const draft: Draft = await (
      await request.post(`/api/projects/${projectId}/workstream-draft`, {
        data: input,
      })
    ).json();
    expect(draft.config.branch).toBe('x'.repeat(64));

    const res = await request.post(`/api/projects/${projectId}/workstreams`, {
      data: input,
    });
    expect(res.status(), await res.text()).toBe(201);
    const created = await awaitRealWorkstream(request, projectId, 20_000);
    expect(created.branch).toBe(draft.config.branch);
    expect(created.workingDir).toBe(draft.config.path);
  });
});

// These create workstreams too, which reads workstreams.location: a create
// while another test has it set to a folder inside the project is refused.
test('a worktree whose kept folder was removed by hand can be made again', async ({
  request,
}) => {
  // The managed folder it is made in again follows workstreams.location.
  await withSettingsLock(async () => {
    const { projectId } = await gitProject(request, 'hs-draft-');
    const input = {
      providerId: 'core',
      name: 'Again',
      config: { strategy: 'worktree' },
    };
    const create = async () => {
      const res = await request.post(`/api/projects/${projectId}/workstreams`, {
        data: input,
      });
      expect(res.status(), await res.text()).toBe(201);
      return awaitRealWorkstream(request, projectId, 20_000);
    };
    const first = await create();
    const deleted = await request.delete(
      `/api/projects/${projectId}/workstreams/${first.id}`,
      { data: { keepFiles: true } },
    );
    expect(deleted.status()).toBe(204);
    const dir = first.workingDir ?? '';
    expect(dir).not.toBe('');
    await rm(dir, { recursive: true, force: true });

    expect((await create()).workingDir).toBe(dir);
  });
});

test('a branch whose kept folder was removed by hand can be taken by another workstream', async ({
  request,
}) => {
  await withSettingsLock(async () => {
    const { projectId } = await gitProject(request, 'hs-draft-');
    const create = async (name: string) => {
      const res = await request.post(`/api/projects/${projectId}/workstreams`, {
        data: {
          providerId: 'core',
          name,
          config: { strategy: 'worktree', branch: 'feat' },
        },
      });
      expect(res.status(), await res.text()).toBe(201);
      return awaitRealWorkstream(request, projectId, 20_000);
    };
    const first = await create('First');
    const deleted = await request.delete(
      `/api/projects/${projectId}/workstreams/${first.id}`,
      { data: { keepFiles: true } },
    );
    expect(deleted.status()).toBe(204);
    await rm(first.workingDir ?? '', { recursive: true, force: true });

    const second = await create('Second');
    expect(second.name).toBe('Second');
    expect(second.branch).toBe('feat');
  });
});

test('a draft for an unknown project is not found', async ({ request }) => {
  const res = await request.post('/api/projects/nope/workstream-draft', {
    data: { providerId: 'core' },
  });
  expect(res.status()).toBe(404);
});
