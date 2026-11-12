import { basename } from 'node:path';
import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { selectProject } from './workstream-helpers';
import { addEmptyProject } from '../projects';
import { createWorkstream } from '../api/workstream-helpers';

/**
 * Renaming a session from the card: the title becomes a text box, Enter commits,
 * and the new name arrives back over the event stream (the daemon owns it — the row
 * does not paint the name locally). Clearing the box hands the session back to its
 * provider's own name.
 */

async function projectWithSession(
  request: APIRequestContext,
): Promise<{ dirName: string; projectId: string; workstreamId: string }> {
  const dir = await tempProjectDir('hs-ui-rename-');
  const projectId = await addEmptyProject(request, dir);

  const workstreamId = await createWorkstream(request, projectId, 'ui-rename');

  await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );
  return { dirName: basename(dir), projectId, workstreamId };
}

async function openCard(page: Page, dirName: string) {
  await page.goto('/');
  await selectProject(page, dirName);
  const card = page.getByRole('region', { name: 'Workstream ui-rename' });
  await expect(card).toBeVisible();
  return card;
}

test('renames a session from its row, and the name comes back from the daemon', async ({
  page,
  request,
}) => {
  const { dirName } = await projectWithSession(request);
  const card = await openCard(page, dirName);

  // The session starts as a placeholder (the e2e fake delays startup) and only the
  // real session is renamable, so wait for a row that is not the placeholder.
  const name = card.getByTestId('session-title');
  await expect(name).toHaveText('Claude Code', { timeout: 15_000 });
  await name.dblclick();

  const box = card.getByLabel('Session name');
  await expect(box).toBeVisible();
  await box.fill('Port the CLI');
  await box.press('Enter');

  // Back to a plain row, under the new name — pushed by the daemon, not painted
  // locally.
  await expect(card.getByLabel('Session name')).toHaveCount(0);
  await expect(card.getByText('Port the CLI')).toBeVisible();

  // And it stays: the provider keeps reporting its own title on every cycle.
  await page.waitForTimeout(1500);
  await expect(card.getByText('Port the CLI')).toBeVisible();

  // A reload reads it back off the daemon rather than any browser state.
  await page.reload();
  const reopened = await openCard(page, dirName);
  await expect(reopened.getByText('Port the CLI')).toBeVisible();
});

test('Escape abandons a rename', async ({ page, request }) => {
  const { dirName } = await projectWithSession(request);
  const card = await openCard(page, dirName);

  const name = card.getByTestId('session-title');
  await expect(name).toHaveText('Claude Code', { timeout: 15_000 });
  await name.dblclick();

  const box = card.getByLabel('Session name');
  await box.fill('never mind');
  await box.press('Escape');

  await expect(card.getByLabel('Session name')).toHaveCount(0);
  await expect(card.getByText('never mind')).toHaveCount(0);
  // The fake provider titles its sessions "Claude Code" — the same text as the
  // agent pill beside them — so the title is asserted through its own test id.
  await expect(card.getByTestId('session-title')).toHaveText('Claude Code');
});

test('clearing the box restores the provider’s own name', async ({
  page,
  request,
}) => {
  const { dirName } = await projectWithSession(request);
  const card = await openCard(page, dirName);

  const name = card.getByTestId('session-title');
  await expect(name).toHaveText('Claude Code', { timeout: 15_000 });
  await name.dblclick();
  await card.getByLabel('Session name').fill('mine');
  await card.getByLabel('Session name').press('Enter');
  await expect(name).toHaveText('mine');

  // Clear it: the daemon drops the custom name and re-projects, so the provider's
  // title is what comes back.
  await name.dblclick();
  await card.getByLabel('Session name').fill('');
  await card.getByLabel('Session name').press('Enter');

  await expect(name).toHaveText('Claude Code');
});

// The reported bug: two sessions could be renamed to the same thing, leaving two
// rows nothing could tell apart. The daemon refuses the second, and the row keeps
// the name it had — the rename is not painted locally, so nothing has to be undone.
test('a second session cannot be renamed onto the first one’s title', async ({
  page,
  request,
}) => {
  const { dirName, projectId, workstreamId } = await projectWithSession(request);
  // A second session in the same workstream, from the same option — so both start
  // out wearing the provider's own title.
  await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );
  const card = await openCard(page, dirName);
  const rows = card.getByTestId('session-title');
  await expect(rows).toHaveCount(2, { timeout: 20_000 });
  await expect(rows.first()).toHaveText('Claude Code', { timeout: 20_000 });
  await expect(rows.last()).toHaveText('Claude Code', { timeout: 20_000 });

  // Name the first one.
  await rows.first().dblclick();
  await card.getByLabel('Session name').fill('Review pass');
  await card.getByLabel('Session name').press('Enter');
  await expect(rows.first()).toHaveText('Review pass');

  // The second cannot take it — case and padding included.
  await rows.last().dblclick();
  await card.getByLabel('Session name').fill('  review PASS  ');
  await card.getByLabel('Session name').press('Enter');

  // The refusal appears against the session it is about — inside that row —
  // rather than on the view's page-level error line at the top of the project.
  const warning = card.getByTestId('session-warning');
  await expect(warning).toBeVisible({ timeout: 10_000 });
  await expect(warning).toContainText(/already called that/i);
  // "Near the session" concretely: the warning is inside a session row, not
  // floating at the top of the project view.
  await expect(
    card.locator('li:has([data-testid="session-warning"])'),
  ).toHaveCount(1);
  const rowWithWarning = card.locator('li:has([data-testid="session-warning"])');
  // And that row still holds what was typed, so it can be corrected in place.
  await expect(rowWithWarning.getByLabel('Session name')).toHaveValue(
    '  review PASS  ',
  );

  // The refusal quotes the title that is in the way, so it appears in the warning
  // as well as on the row holding it — assert the *titles*, not the card's text.
  await expect(warning).toContainText('Review pass');
  const titles = card.getByTestId('session-title');
  await expect(titles).toHaveCount(1); // the other row is still in its box
  await expect(titles).toHaveText('Review pass');

  // A different name still works, so the rule is not a freeze — and the warning
  // goes with the value it was about, the moment that value changes.
  //
  // Located from the card rather than from `rowWithWarning`: that locator selects
  // the row *by* the warning, and typing is what clears it, so it would stop
  // matching halfway through this block. Only one box is open (asserted above).
  const box = card.getByLabel('Session name');
  await box.fill('Second pass');
  await expect(card.getByTestId('session-warning')).toHaveCount(0);
  await box.press('Enter');
  await expect(rows.last()).toHaveText('Second pass');
});
