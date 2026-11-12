import { basename } from 'node:path';
import { type APIRequestContext } from '@playwright/test';
import { expect, test } from '../fixtures';
import { gitProject } from '../git-repo';
import { withSettingsLock } from '../settings-lock';
import { createProject } from './workstream-helpers';

/**
 * Creation config the daemon refuses **on the request**.
 *
 * Creation is otherwise asynchronous, but a value that can never work — `git
 * worktree add` will not take a branch called `my branch` — is refused before
 * anything is reserved: 400, field-level detail, and nothing created, rather
 * than a workstream that fails in the background and is left in `error`.
 * Each project is git-backed, since the worktree strategy is only offered on
 * one.
 */

const createWorktree = (
  request: APIRequestContext,
  projectId: string,
  branch: string,
) =>
  request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      config: { strategy: 'worktree', branch },
    },
  });

async function workstreamsOf(request: APIRequestContext, projectId: string) {
  const state = await (await request.get('/api/projects')).json();
  return (
    state.projects?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams ?? []
  );
}

/** Waits until `count` of the project's creates have landed as real workstreams. */
async function awaitLanded(
  request: APIRequestContext,
  projectId: string,
  count: number,
) {
  await expect
    .poll(
      async () =>
        (await workstreamsOf(request, projectId)).filter(
          (workstream: { id: string }) =>
            !workstream.id.startsWith('creating-'),
        ).length,
      { timeout: 15_000 },
    )
    .toBe(count);
}

test('an illegal branch name is refused on the request, not after the fact', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-cfg-valid-');

  const res = await createWorktree(request, projectId, 'my branch');
  expect(res.status()).toBe(400);

  const body = await res.json();
  // Field-level, keyed by JSON pointer, so a client can blame the right input.
  expect(body.fields?.['/branch']).toBeTruthy();
  expect(body.fields['/branch']).toContain('my branch');

  // Nothing was created, not even a placeholder.
  expect(await workstreamsOf(request, projectId)).toHaveLength(0);
});

test('every shape git refuses is refused here', async ({ request }) => {
  const { projectId } = await gitProject(request, 'hs-cfg-valid-');

  const illegal = [
    'my branch', // space
    'my:branch', // colon
    'my~branch', // tilde
    'my^branch', // caret
    'my?branch', // question mark
    'my*branch', // asterisk
    'my[branch', // bracket
    'my..branch', // double dot
    'my.lock', // reserved suffix
    'my@{branch', // reflog syntax
    '.hidden', // leading dot
    '-flag', // leading dash
    'branch/', // trailing slash
  ];
  for (const branch of illegal) {
    const res = await createWorktree(request, projectId, branch);
    expect(res.status(), `branch ${JSON.stringify(branch)}`).toBe(400);
  }
  expect(await workstreamsOf(request, projectId)).toHaveLength(0);
});

test('a legal branch is still accepted', async ({ request }) => {
  const { projectId } = await gitProject(request, 'hs-cfg-valid-');

  const res = await createWorktree(request, projectId, 'fix/self-update');
  expect(res.status()).toBe(201);
});

// git would read it as an option: `--no-checkout` would quietly make an empty
// worktree rather than fail.
test('a base ref that starts with a dash is refused, blaming the base ref', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-cfg-valid-');

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      config: { strategy: 'worktree', branch: 'b', baseRef: '--no-checkout' },
    },
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).fields?.['/baseRef']).toContain('--no-checkout');
  expect(await workstreamsOf(request, projectId)).toHaveLength(0);
});

// The branch only means anything to the worktree strategy, so validating it
// under another one would refuse a create it cannot affect.
test('a branch is ignored by a strategy that does not use it', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-cfg-valid-');

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      config: { strategy: 'in-place', branch: 'my branch' },
    },
  });
  expect(res.status()).toBe(201);
});

test('a relative destination path is refused, blaming the path', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-cfg-valid-');

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      config: { strategy: 'copy', path: '../somewhere' },
    },
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).fields?.['/path']).toBeTruthy();
  expect(await workstreamsOf(request, projectId)).toHaveLength(0);
});

test('a copy inside the project it copies is refused, blaming the path', async ({
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-cfg-valid-');

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      config: { strategy: 'copy', path: `${dir}/copies/x` },
    },
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).fields?.['/path']).toContain(
    'inside the project it copies',
  );
  expect(await workstreamsOf(request, projectId)).toHaveLength(0);
});

// With the path left blank, the location setting put the copy there, so the
// refusal names the setting rather than leaving the user to guess.
test('a copy the location setting puts inside the project names the setting', async ({
  request,
}) => {
  await withSettingsLock(async () => {
    const { projectId } = await gitProject(request, 'hs-cfg-valid-');
    const current = await (await request.get('/api/settings')).json();
    const put = await request.put('/api/settings', {
      data: {
        ...current,
        workstreams: { ...current.workstreams, location: 'copies' },
      },
    });
    expect(put.status()).toBe(200);
    try {
      const res = await request.post(`/api/projects/${projectId}/workstreams`, {
        data: { providerId: 'core', config: { strategy: 'copy' } },
      });
      expect(res.status()).toBe(400);
      expect((await res.json()).fields?.['/path']).toContain(
        'workstreams location setting ("copies")',
      );
      expect(await workstreamsOf(request, projectId)).toHaveLength(0);
    } finally {
      await request.put('/api/settings', { data: current });
    }
  });
});

// A branch left empty is derived from the name, and a name that yields none
// is refused on the branch rather than failing later in git.
test('a worktree whose name yields no branch is refused on the branch', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-cfg-valid-');

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'core', name: '???', config: { strategy: 'worktree' } },
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).fields?.['/branch']).toContain('???');
  expect(await workstreamsOf(request, projectId)).toHaveLength(0);
});

// The schema's own refusals are worded for a person, on the field they blame,
// not in the validator's keyword notation.
test('a config the schema refuses says why in words, on the field', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-cfg-words-');
  const create = (config: Record<string, unknown>) =>
    request.post(`/api/projects/${projectId}/workstreams`, {
      data: { providerId: 'fake', config },
    });

  for (const config of [{ branch: '' }, {}]) {
    const res = await create(config);
    expect(res.status(), JSON.stringify(config)).toBe(400);
    expect((await res.json()).fields).toEqual({ '/branch': 'required' });
  }

  const wrongType = await create({ branch: 'main', copyEnv: 'yes' });
  expect(wrongType.status()).toBe(400);
  expect((await wrongType.json()).fields).toEqual({
    '/copyEnv': 'must be true or false',
  });
  expect(await workstreamsOf(request, projectId)).toHaveLength(0);
});

// The repository's own state, asked on the request too: a branch already
// checked out cannot be put in a worktree, and a new branch has to start from a
// ref the repository has. The project's `feature` branch is checked out in it.
test('a branch checked out already is refused, blaming the branch', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-cfg-valid-');

  const res = await createWorktree(request, projectId, 'feature');
  expect(res.status()).toBe(400);
  expect((await res.json()).fields?.['/branch']).toBe(
    'branch "feature" is already checked out in the project folder; enter another',
  );
  expect(await workstreamsOf(request, projectId)).toHaveLength(0);
});

// A branch left empty is the name's, and the refusal says so: the user typed a
// name, not a branch, and needs to know which input to change.
test('a checked-out branch derived from the name says it came from the name', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-cfg-valid-');

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      name: 'feature',
      config: { strategy: 'worktree' },
    },
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).fields?.['/branch']).toBe(
    'branch "feature" (from the name) is already checked out in the project folder; enter another',
  );
  expect(await workstreamsOf(request, projectId)).toHaveLength(0);
});

test('a base ref the repository lacks is refused, blaming the base ref', async ({
  request,
}) => {
  const { projectId } = await gitProject(request, 'hs-cfg-valid-');

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      config: { strategy: 'worktree', branch: 'topic', baseRef: 'no-such-ref' },
    },
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).fields?.['/baseRef']).toBe(
    'unknown ref "no-such-ref"',
  );
  expect(await workstreamsOf(request, projectId)).toHaveLength(0);
});

// The name a blank form defaults to is numbered past the ones taken, and so is
// the branch it gives, so the same blank form works every time.
test('a blank worktree form can be sent twice', async ({ request }) => {
  const { projectId } = await gitProject(request, 'hs-cfg-blank-');
  const createBlank = () =>
    request.post(`/api/projects/${projectId}/workstreams`, {
      data: { providerId: 'core', config: { strategy: 'worktree' } },
    });

  for (let count = 1; count <= 2; count++) {
    expect((await createBlank()).status()).toBe(201);
    await expect
      .poll(
        async () =>
          (await workstreamsOf(request, projectId)).filter(
            (workstream: { id: string; runtimeState: string }) =>
              !workstream.id.startsWith('creating-') ||
              workstream.runtimeState === 'error',
          ).length,
        { timeout: 15_000 },
      )
      .toBe(count);
  }
  const workstreams = await workstreamsOf(request, projectId);
  expect(
    workstreams.filter((workstream: { id: string }) =>
      workstream.id.startsWith('creating-'),
    ),
  ).toEqual([]);
  const [first, second] = workstreams as { name: string; branch: string }[];
  expect(second.name).toBe(`${first.name}-2`);
  expect(second.branch).not.toBe(first.branch);
});

// A blank name is numbered when the create is accepted, not when it lands, so a
// second blank create sent while the first is still in flight is not refused as
// a duplicate of a shared placeholder name.
test('blank creates sent back to back are numbered apart', async ({
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-cfg-blank-');
  const createBlank = () =>
    request.post(`/api/projects/${projectId}/workstreams`, {
      data: { providerId: 'core', config: { strategy: 'worktree' } },
    });

  const first = await createBlank();
  const second = await createBlank();
  expect(first.status()).toBe(201);
  expect(second.status()).toBe(201);
  const base = basename(dir);
  expect((await first.json()).name).toBe(base);
  expect((await second.json()).name).toBe(`${base}-2`);
  await awaitLanded(request, projectId, 2);
});

test('concurrent blank creates each get a name of their own', async ({
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-cfg-blank-');
  const responses = await Promise.all(
    [1, 2, 3].map(() =>
      request.post(`/api/projects/${projectId}/workstreams`, {
        data: { providerId: 'core', config: { strategy: 'worktree' } },
      }),
    ),
  );
  for (const res of responses) expect(res.status()).toBe(201);
  const names = await Promise.all(
    responses.map(async (res) => (await res.json()).name as string),
  );
  const base = basename(dir);
  expect(names.sort()).toEqual([base, `${base}-2`, `${base}-3`]);
  await awaitLanded(request, projectId, 3);
});

test('a workstream called "New workstream" does not block a blank create', async ({
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-cfg-blank-');
  const named = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      name: 'New workstream',
      config: { strategy: 'worktree', branch: 'topic' },
    },
  });
  expect(named.status()).toBe(201);

  const blank = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'core', config: { strategy: 'worktree' } },
  });
  expect(blank.status()).toBe(201);
  expect((await blank.json()).name).toBe(basename(dir));
  await awaitLanded(request, projectId, 2);
});

// The project folder is one workstream's at most. A second in-place create is
// refused on the request, on the strategy, naming the one already there — not
// accepted and left to fail on a card after the form has closed.
test('a second in-place workstream is refused on the strategy', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-cfg-inplace-');
  const createInPlace = (name: string) =>
    request.post(`/api/projects/${projectId}/workstreams`, {
      data: { providerId: 'core', name, config: { strategy: 'in-place' } },
    });

  expect((await createInPlace('first')).status()).toBe(201);
  await awaitLanded(request, projectId, 1);

  const res = await createInPlace('second');
  expect(res.status()).toBe(400);
  expect((await res.json()).fields).toEqual({
    '/strategy': '"first" already works in this folder; choose copy',
  });
  expect(await workstreamsOf(request, projectId)).toHaveLength(1);
});

test('a destination another workstream works in is refused on the path', async ({
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-cfg-inplace-');
  expect(
    (
      await request.post(`/api/projects/${projectId}/workstreams`, {
        data: {
          providerId: 'core',
          name: 'here',
          config: { strategy: 'in-place' },
        },
      })
    ).status(),
  ).toBe(201);
  await awaitLanded(request, projectId, 1);

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'core', config: { strategy: 'copy', path: dir } },
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).fields?.['/path']).toBe(
    '"here" already works in this folder; enter another',
  );
});
