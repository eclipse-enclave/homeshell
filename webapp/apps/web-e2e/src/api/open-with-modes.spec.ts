import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, test, type APIRequestContext } from '@playwright/test';
import { addEmptyProject, addProjectWithDefault } from '../projects';
import { withSettingsLock } from '../settings-lock';

/**
 * Where the user's "open with" entries meet a workstream: each row becomes an
 * open mode, opened with `POST …/open-with`.
 */

interface Mode {
  id: string;
  label: string;
  default?: boolean;
}

interface Workstream {
  id: string;
  openModes?: Mode[];
}

interface Entry {
  id: string;
  label: string;
  command: string;
  inTerminal: boolean;
}

// These tests share one settings document, so they run in order in one worker.
test.describe.configure({ mode: 'default' });

const unique = (name: string) =>
  `${name}-${Math.random().toString(36).slice(2, 8)}`;

async function addEntry(
  request: APIRequestContext,
  entry: Omit<Entry, 'id'>,
): Promise<string> {
  const doc = await (await request.get('/api/settings')).json();
  doc.openWith.entries = [...doc.openWith.entries, entry];
  const put = await request.put('/api/settings', { data: doc });
  expect(put.status()).toBe(200);
  const echoed = (await put.json()).openWith.entries as Entry[];
  const added = echoed.find((row) => row.label === entry.label);
  expect(added?.id).toBeTruthy();
  return added!.id;
}

async function removeEntry(request: APIRequestContext, id: string) {
  const doc = await (await request.get('/api/settings')).json();
  doc.openWith.entries = (doc.openWith.entries as Entry[]).filter(
    (row) => row.id !== id,
  );
  expect((await request.put('/api/settings', { data: doc })).status()).toBe(200);
}

/**
 * A workstream whose files really are on this host: the in-place one a project
 * comes with.
 */
async function materializedWorkstream(request: APIRequestContext): Promise<{
  projectId: string;
  workstreamId: string;
}> {
  const dir = await mkdtemp(join(tmpdir(), 'hs-openwith-modes-e2e-'));
  const { projectId, workstream } = await addProjectWithDefault(request, dir);
  return { projectId, workstreamId: workstream.id };
}

/** Polls the projection until a workstream in the project satisfies `ready`. */
async function awaitWorkstream(
  request: APIRequestContext,
  projectId: string,
  ready: (ws: Workstream) => boolean,
  timeoutMs = 8000,
): Promise<Workstream> {
  const deadline = Date.now() + timeoutMs;
  let last: Workstream | undefined;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const workstreams: Workstream[] =
      state.projects?.find((p: { id: string }) => p.id === projectId)
        ?.workstreams ?? [];
    for (const ws of workstreams) {
      last = ws;
      if (ready(ws)) return ws;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(
    `timed out waiting for a workstream in ${projectId}; last saw ${JSON.stringify(last)}`,
  );
}

const hasMode = (ws: Workstream, id: string) =>
  (ws.openModes ?? []).some((mode) => mode.id === id);

test('an entry the user adds becomes an item in the Open menu', async ({
  request,
}) => {
  await withSettingsLock(async () => {
  const { projectId, workstreamId } = await materializedWorkstream(request);
  const label = unique('GVim');
  const entryId = await addEntry(request, {
    label,
    command: 'gvim .',
    inTerminal: false,
  });

  const withEntry = await awaitWorkstream(
    request,
    projectId,
    (ws) => ws.id === workstreamId && hasMode(ws, `open-with:${entryId}`),
  );
  const mode = (withEntry.openModes ?? []).find(
    (m) => m.id === `open-with:${entryId}`,
  );
  expect(mode?.label).toBe(label);
  expect(hasMode(withEntry, 'open-with:terminal')).toBe(true);
  // The plain open runs the terminal row, and the files are here to open.
  const defaults = (withEntry.openModes ?? []).filter((mode) => mode.default);
  expect(defaults.map((mode) => mode.id)).toEqual(['open-with:terminal']);

  await removeEntry(request, entryId);

  await awaitWorkstream(
    request,
    projectId,
    (ws) => ws.id === workstreamId && !hasMode(ws, `open-with:${entryId}`),
  );
  });
});

// Only the menu item is asserted: launching a terminal row opens a real window.
test('a row marked "in a terminal" is an item in the Open menu too', async ({
  request,
}) => {
  await withSettingsLock(async () => {
  const { projectId, workstreamId } = await materializedWorkstream(request);
  const label = unique('Neovim');
  const entryId = await addEntry(request, {
    label,
    command: 'nvim .',
    inTerminal: true,
  });

  try {
    const withEntry = await awaitWorkstream(
      request,
      projectId,
      (ws) => ws.id === workstreamId && hasMode(ws, `open-with:${entryId}`),
    );
    const mode = (withEntry.openModes ?? []).find(
      (m) => m.id === `open-with:${entryId}`,
    );
    expect(mode?.label).toBe(label);
  } finally {
    await removeEntry(request, entryId);
  }
  });
});

// Safe to open for real: the program does not exist, so no window appears.
test('opening a detached entry starts its shell line in the directory', async ({
  request,
}) => {
  await withSettingsLock(async () => {
  const { projectId, workstreamId } = await materializedWorkstream(request);
  const label = unique('Detached');
  const entryId = await addEntry(request, {
    label,
    command: 'homeshell-no-such-program .',
    inTerminal: false,
  });
  try {
    await awaitWorkstream(
      request,
      projectId,
      (ws) => ws.id === workstreamId && hasMode(ws, `open-with:${entryId}`),
    );
    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${workstreamId}/open-with`,
      { data: { mode: `open-with:${entryId}` } },
    );
    expect(res.status()).toBe(200);
    const body = await res.json();
    expect(body.mode).toBe(`open-with:${entryId}`);
    expect(body.data.target).toBe('host');
    expect(body.data.command).toBe('homeshell-no-such-program .');
    expect(body.data.workingDir).toBeTruthy();
  } finally {
    await removeEntry(request, entryId);
  }
  });
});

test('entries are not offered for a workstream whose files are elsewhere', async ({
  request,
}) => {
  await withSettingsLock(async () => {
  const dir = await mkdtemp(join(tmpdir(), 'hs-openwith-nofiles-e2e-'));
  const projectId = await addEmptyProject(request, dir);
  // The fake provider records a working directory and never creates it.
  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'nofiles', config: { branch: 'main' } },
  });
  expect(created.status()).toBe(201);
  const workstream = await awaitWorkstream(
    request,
    projectId,
    (ws) => !ws.id.startsWith('creating-'),
  );

  const label = unique('Nautilus');
  const entryId = await addEntry(request, {
    label,
    command: 'nautilus .',
    inTerminal: false,
  });
  try {
    const after = await awaitWorkstream(
      request,
      projectId,
      (ws) => ws.id === workstream.id,
    );
    expect(hasMode(after, `open-with:${entryId}`)).toBe(false);

    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${workstream.id}/open-with`,
      { data: { mode: `open-with:${entryId}` } },
    );
    expect(res.status()).toBe(409);
    expect((await res.json()).error).toContain('working directory');
  } finally {
    await removeEntry(request, entryId);
  }
  });
});

test('a mode naming an entry that is gone is refused', async ({ request }) => {
  await withSettingsLock(async () => {
  const { projectId, workstreamId } = await materializedWorkstream(request);
  const entryId = await addEntry(request, {
    label: unique('Doomed'),
    command: 'gvim .',
    inTerminal: false,
  });
  await removeEntry(request, entryId);

  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/open-with`,
    { data: { mode: `open-with:${entryId}` } },
  );
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toBeTruthy();
  });
});
