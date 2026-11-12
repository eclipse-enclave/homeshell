import { basename } from 'node:path';
import { type APIRequestContext, type Locator, type Page } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { selectProject } from './workstream-helpers';
import { addEmptyProject } from '../projects';
import { createWorkstream } from '../api/workstream-helpers';

/**
 * A session's output has to read as the *session* speaking, never as HomeShell.
 *
 * The report this exists for: a codex session showed `Press enter to continue`
 * and it was taken for the app asking the user to press enter. Two things made
 * that possible — the collapsed teaser was loose text at card level, and the
 * expanded output box was the same rectangle as HomeShell's own send-text input
 * directly beneath it. So the assertions here are about *separation*: the output
 * is painted on a surface of its own, unlike the card it sits in and unlike the
 * control it sits above.
 */

// The callback runs in the browser but is type-checked against this project's
// tsconfig, which has no DOM lib (`lib: ["es2022"]`, `types: node`) — hence
// globalThis rather than the bare global, as in session-startup.spec.ts.
const backgroundOf = (locator: Locator): Promise<string> =>
  locator.evaluate(
    (el) =>
      (
        globalThis as unknown as {
          getComputedStyle: (e: unknown) => { backgroundColor: string };
        }
      ).getComputedStyle(el).backgroundColor,
  );

async function sessionWithOutput(
  request: APIRequestContext,
  page: Page,
): Promise<Locator> {
  const dir = await tempProjectDir('hs-ui-output-');
  const projectId = await addEmptyProject(request, dir);

  const workstreamId = await createWorkstream(request, projectId, 'ui-output');

  await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'codex', config: {} } },
  );
  const deadline = Date.now() + 15_000;
  let sessionId = '';
  while (Date.now() < deadline && !sessionId) {
    const state = await (await request.get('/api/projects')).json();
    sessionId =
      state.projects
        ?.find((p: { id: string }) => p.id === projectId)
        ?.workstreams?.find((w: { id: string }) => w.id === workstreamId)
        ?.sessions?.find((s: { id: string }) => !s.id.startsWith('starting-'))
        ?.id ?? '';
    if (!sessionId) await new Promise((r) => setTimeout(r, 50));
  }
  expect(sessionId).toBeTruthy();

  // The provider echoes a prompt into its preview, which is how a spec gets a
  // known line of "agent output" to look at.
  await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions/${sessionId}/prompt`,
    { data: { text: 'Press enter to continue' } },
  );

  await page.goto('/');
  await selectProject(page, basename(dir));
  const card = page.getByRole('region', { name: 'Workstream ui-output' });
  await expect(card).toBeVisible();
  return card;
}

test('the collapsed tail sits on a surface of its own, not on the card', async ({
  page,
  request,
}) => {
  const card = await sessionWithOutput(request, page);

  const teaser = card.getByTestId('session-preview');
  await expect(teaser).toContainText('Press enter to continue', {
    timeout: 20_000,
  });

  // Loose text would inherit the card. This must not.
  const [onTeaser, onCard] = await Promise.all([
    backgroundOf(teaser),
    backgroundOf(card),
  ]);
  expect(onTeaser).not.toBe(onCard);
  expect(onTeaser).not.toBe('rgba(0, 0, 0, 0)');
});

test('the expanded output does not look like the box you type into', async ({
  page,
  request,
}) => {
  const card = await sessionWithOutput(request, page);
  await expect(card.getByTestId('session-preview')).toContainText(
    'Press enter to continue',
    { timeout: 20_000 },
  );

  await card.getByRole('button', { name: 'Toggle session details' }).click();
  const output = card.getByTestId('session-preview-full');
  await expect(output).toBeVisible();

  // Named on screen, so the reader who opened the row to read it closely is told
  // whose text it is.
  await expect(card.getByText('Session output')).toBeVisible();

  // And painted unlike the send-text input immediately below it — the pair that
  // were previously the same rectangle.
  const input = card.getByLabel('Send text to session');
  await expect(input).toBeVisible();
  const [onOutput, onInput] = await Promise.all([
    backgroundOf(output),
    backgroundOf(input),
  ]);
  expect(onOutput).not.toBe(onInput);
});

test('the separation survives a theme change', async ({ page, request }) => {
  const card = await sessionWithOutput(request, page);
  await expect(card.getByTestId('session-preview')).toContainText(
    'Press enter to continue',
    { timeout: 20_000 },
  );

  // Both themes define the terminal tokens; a theme that forgot one would leave
  // the output inheriting the card again, which is the bug this guards.
  for (let i = 0; i < 2; i++) {
    await page.getByRole('button', { name: /theme/i }).click();
    const teaser = card.getByTestId('session-preview');
    await expect(teaser).toBeVisible();
    const [onTeaser, onCard] = await Promise.all([
      backgroundOf(teaser),
      backgroundOf(card),
    ]);
    expect(onTeaser).not.toBe(onCard);
    expect(onTeaser).not.toBe('rgba(0, 0, 0, 0)');
  }
});
