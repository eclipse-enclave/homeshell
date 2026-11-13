import { type APIRequestContext, type Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { loadApp } from './keyboard-helpers';
import {
  selectProject,
  sessionRows,
  workstreamWithEndedSessions,
} from './workstream-helpers';

/**
 * A workstream card lists its live sessions first, then its ended ones under
 * "Ended · N", the newest three shown and the rest folded away. An ended
 * session continues from its Continue button.
 */

/** Loads the app on a fresh workstream with these sessions; returns its card. */
async function openCard(
  page: Page,
  request: APIRequestContext,
  sessions: { ended: number; running: number },
) {
  const created = await workstreamWithEndedSessions(
    request,
    'hs-ui-ended',
    'ended',
    sessions,
  );
  await loadApp(page);
  await selectProject(page, created.projectPrefix);
  const card = page.getByRole('region', {
    name: `Workstream ${created.workstreamName}`,
  });
  return { ...created, card };
}

test('an ended session continues from its Continue button', async ({
  page,
  request,
}) => {
  const { card } = await openCard(page, request, { ended: 2, running: 1 });
  await expect(card.getByText('Ended · 2')).toBeVisible({ timeout: 15_000 });
  const row = sessionRows(card)
    .filter({ has: page.getByRole('button', { name: 'Continue' }) })
    .first();

  // A click on the row shows its details and says exactly when it ended.
  await row.getByTestId('session-title').click();
  await expect(
    row.getByRole('button', { expanded: true }).first(),
  ).toBeVisible();
  await expect(row.getByText('just now')).toHaveAttribute('title', /\d/);
  await expect(card.getByText('Ended · 2')).toBeVisible();

  await row.getByRole('button', { name: 'Continue' }).click();
  await expect(card.getByText('Ended · 1')).toBeVisible({ timeout: 15_000 });
  await expect(card.getByTestId('workstream-sessions')).toHaveAttribute(
    'aria-label',
    'Sessions: 2 running, 1 ended',
  );
});

test('Enter on an ended row shows its details and leaves it ended', async ({
  page,
  request,
}) => {
  const { card } = await openCard(page, request, { ended: 1, running: 0 });
  await expect(card.getByText('Ended · 1')).toBeVisible({ timeout: 15_000 });
  const row = sessionRows(card).first();
  await card.focus();
  await page.keyboard.press('End');
  await expect(row).toBeFocused();

  await page.keyboard.press('Enter');
  await expect(
    row.getByRole('button', { name: 'Toggle session details' }),
  ).toHaveAttribute('aria-expanded', 'true');
  await expect(card.getByText('Ended · 1')).toBeVisible();
});

test('the fold opens from the keyboard, onto the first row it held', async ({
  page,
  request,
}) => {
  const { card } = await openCard(page, request, { ended: 4, running: 1 });
  const fold = card.getByRole('button', { name: '1 more ended session' });
  await expect(fold).toBeVisible({ timeout: 15_000 });
  const ended = sessionRows(card).filter({
    has: page.getByRole('button', { name: 'Continue' }),
  });
  await card.focus();
  await page.keyboard.press('End');
  await expect(ended.last()).toBeFocused();
  // Past the row's title, Continue, More actions and details toggle.
  for (let i = 0; i < 5; i++) await page.keyboard.press('Tab');
  await expect(fold).toBeFocused();

  await page.keyboard.press('Enter');
  await expect(ended).toHaveCount(4);
  await expect(ended.nth(3)).toBeFocused();
  // The list's one tab stop moved with focus.
  await expect(
    page.getByRole('main').locator('[data-nav-id][tabindex="0"]'),
  ).toHaveCount(1);
});

test('a focused session that stops keeps the selection and focus', async ({
  page,
  request,
}) => {
  const { card, paths } = await openCard(page, request, {
    ended: 0,
    running: 2,
  });
  expect(
    (await request.patch(paths[0], { data: { title: 'Watched' } })).status(),
  ).toBe(204);

  const row = card.getByRole('listitem').filter({ hasText: 'Watched' });
  await expect(row.getByRole('button', { name: 'Stop' })).toBeVisible({
    timeout: 15_000,
  });
  await row.focus();
  await expect(row).toHaveAttribute('aria-current', 'true');

  expect((await request.post(`${paths[0]}/stop`)).status()).toBe(204);
  await expect(row.getByRole('button', { name: 'Continue' })).toBeVisible({
    timeout: 15_000,
  });
  // Last in the card, below the divider, and still the one the list is on.
  await expect(card.getByRole('listitem').last()).toContainText('Watched');
  await expect(row).toHaveAttribute('aria-current', 'true');
  await expect(row).toBeFocused();
});
