import { expect, test } from '@playwright/test';

import { awaitRealWorkstream, createProject } from './workstream-helpers';

/**
 * POST /api/projects/{id}/workstreams/{wsId}/note — the workstream note: the
 * daemon creates NOTES.md in the workstream's working directory (where its
 * sessions run, so the agent can read it) and hands it to the desktop's default
 * application.
 *
 * Only the refusals are asserted here. A successful note opens a real editor
 * window on whoever runs `make test-e2e` — the same reason the host terminal and
 * desktop IDE branches are not e2e-tested. Creating the
 * file, leaving an existing one alone, and a hand-off that fails because the
 * host has no desktop opener are covered by unit tests that inject a recording
 * opener: daemon/internal/provider/note_test.go and
 * daemon/internal/api/workstreams_note_test.go.
 */

test('refuses a note for a workstream with no files on the host', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-note-e2e-');
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'noteless', config: { branch: 'main' } },
  });
  // The fake provider records a working directory but never creates it, so
  // there is nowhere to keep a note — which is also what stops this test from
  // opening an editor on the machine running it. The daemon says as much up
  // front by not advertising the action.
  const workstream = await awaitRealWorkstream(request, projectId);
  expect(workstream.actions?.map((action) => action.id) ?? []).not.toContain(
    'note',
  );

  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstream.id}/note`,
  );
  expect(res.status()).toBe(409);
  expect((await res.json()).error).toContain('working directory');
});

test('a note for an unknown workstream is a 404', async ({ request }) => {
  const projectId = await createProject(request, 'hs-note-e2e-');

  const res = await request.post(
    `/api/projects/${projectId}/workstreams/ghost/note`,
  );
  expect(res.status()).toBe(404);
});

// One file of notes per workstream, so the action is named in the plural.
test('a workstream with files on the host offers its notes, so labelled', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-note-e2e-');
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'core', config: { strategy: 'copy' } },
  });
  expect(res.status()).toBe(201);
  const workstream = await awaitRealWorkstream(request, projectId, 15_000);
  expect(
    workstream.actions?.find((action) => action.id === 'note')?.label,
  ).toBe('Notes');
});
