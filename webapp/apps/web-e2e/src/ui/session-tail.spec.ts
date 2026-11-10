import { expect, test } from '../fixtures';

/**
 * The terminal-tail preview and send-text flow against the demo daemon. Both
 * tests write into one seeded session's preview, so they run in order.
 */

// Both tests below drive that one seeded session on the one shared demo daemon,
// and each writes to its preview. Under the project's `fullyParallel` they land
// in different workers, where the arrow-key test's `[key: down]` overwrites the
// preview the send-text test is asserting its own `e2e-tail-<n>` into — last
// writer wins, and the failure surfaces as the other test's echo. Opting out of
// parallelism keeps them in one worker, in order, which is the fix for a shared
// fixture that cannot be given one instance per test.
test.describe.configure({ mode: 'default' });
test('session tail: send text echoes into the preview', async ({ page }) => {
  await page.goto('/');

  // Scoped to one workstream card and one session row so the toggle, send input
  // and preview selectors stay unambiguous beside sibling rows.
  const workstream = page.getByRole('region', {
    name: 'Workstream feature/self-update',
  });
  await expect(workstream).toBeVisible();

  const row = workstream
    .getByRole('listitem')
    .filter({ hasText: 'Implement self-update' });
  await expect(row).toBeVisible();

  await row.getByRole('button', { name: /toggle session details/i }).click();

  // The fake provider advertises oneOffPrompt, so the send input is shown.
  const input = row.getByLabel(/send text to session/i);
  await expect(input).toBeVisible();

  // Distinctive so the echo is unambiguous across reruns on a reused daemon.
  const text = `e2e-tail-${Date.now()}`;
  await input.fill(text);
  await row.getByRole('button', { name: /^send$/i }).click();

  await expect(input).toHaveValue('');

  // The fake echoes `"> <text>"` into the preview, which arrives over SSE.
  const full = row.getByTestId('session-preview-full');
  await expect(full).toContainText(`> ${text}`);

  // Collapsed, the row shows the teaser: the last preview line.
  await row.getByRole('button', { name: /toggle session details/i }).click();
  const teaser = row.getByTestId('session-preview');
  await expect(teaser).toBeVisible();
  await expect(teaser).toContainText(text);
});

test('session tail: an arrow key navigates via the empty input', async ({
  page,
}) => {
  await page.goto('/');

  const workstream = page.getByRole('region', {
    name: 'Workstream feature/self-update',
  });
  await expect(workstream).toBeVisible();
  const row = workstream
    .getByRole('listitem')
    .filter({ hasText: 'Implement self-update' });
  await expect(row).toBeVisible();

  await row.getByRole('button', { name: /toggle session details/i }).click();

  const input = row.getByLabel(/send text to session/i);
  await expect(input).toBeVisible();
  // The hint is shown because the provider supports key navigation.
  await expect(row.getByText(/navigate/i)).toBeVisible();

  // The fake echoes "[key: down]" into the preview.
  await input.focus();
  await input.press('ArrowDown');

  const full = row.getByTestId('session-preview-full');
  await expect(full).toContainText('[key: down]');
});
