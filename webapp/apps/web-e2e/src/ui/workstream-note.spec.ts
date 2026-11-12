import { basename } from 'node:path';
import { expect, tempProjectDir, test } from '../fixtures';
import {
  materializedWorkstream,
  selectProject,
  unique,
} from './workstream-helpers';
import { addEmptyProject } from '../projects';

/**
 * The workstream note in the browser: the card renders the actions the daemon
 * advertises in its own footer, and clicking one asks the daemon to run it.
 *
 * The POST is intercepted rather than let through. A real one succeeds, and
 * succeeding means an application opens on whoever runs `make test-e2e` — the
 * same reason the Open menu is never clicked through (see open-with-settings).
 * What the click has to prove is the wiring, and the route sees that.
 */

test('the note the daemon advertises is offered, and clicking it asks for the note', async ({
  page,
  request,
}) => {
  const { dirName, workstreamName, projectId, workstreamId } =
    await materializedWorkstream(request, 'hs-ui-note-', 'note-ws');

  const noteUrl = `**/api/projects/${projectId}/workstreams/${workstreamId}/note`;
  const posted: string[] = [];
  await page.route(noteUrl, async (route) => {
    posted.push(route.request().method());
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ path: '/tmp/NOTES.md', created: true }),
    });
  });

  await page.goto('/');
  await selectProject(page, dirName);
  const card = page.getByRole('region', {
    name: `Workstream ${workstreamName}`,
  });
  await expect(card).toBeVisible();

  const note = card.getByRole('button', {
    name: `Notes for workstream ${workstreamName}`,
  });
  await expect(note).toBeVisible();
  await note.click();

  await expect.poll(() => posted).toEqual(['POST']);
  // A 200 is not an error, so nothing is reported to the user. Scoped to the
  // project panel: the demo daemon's notification toasts are alerts too, and
  // one arriving on its own timer is not this test's business.
  await expect(page.getByRole('main').getByRole('alert')).toHaveCount(0);
});

// The note lives among the workstream's files. A fake-provider workstream has a
// working directory the provider never created, so the daemon offers no note and
// the footer has nothing to render.
test('a workstream with no files on the host offers no note', async ({
  page,
  request,
}) => {
  const dir = await tempProjectDir('hs-ui-note-none-');
  const projectId = await addEmptyProject(request, dir);
  const workstreamName = unique('noteless');
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'fake',
      name: workstreamName,
      config: { branch: 'main' },
    },
  });

  await page.goto('/');
  await selectProject(page, basename(dir));
  const card = page.getByRole('region', {
    name: `Workstream ${workstreamName}`,
  });
  await expect(card).toBeVisible();
  await expect(
    card.getByRole('button', {
      name: `Notes for workstream ${workstreamName}`,
    }),
  ).toHaveCount(0);
});
