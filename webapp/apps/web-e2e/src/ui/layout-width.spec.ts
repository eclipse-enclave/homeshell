import { basename } from 'node:path';
import { type APIRequestContext, type Locator, type Page } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { selectProject } from './workstream-helpers';

/**
 * How the project view spends horizontal space.
 *
 * Workstream cards fill whatever the layout gives them: a card is a row of
 * controls with a spacer in the middle, and the session output it holds is
 * monospace terminal text, so width is useful rather than wasteful. The view used
 * to cap itself at 56rem, which left a third of a laptop screen empty.
 *
 * The create forms follow the same rule. The new-workstream form sits in the
 * list beside the cards and spans it as they do; the new-session form, panel and
 * fields, is a band in the session list, spanning the card like the rows it sits
 * above. Asserting all four keeps a cap from being reintroduced on the view, on
 * either form, or on the fields inside one.
 */

// Wide enough that a 56rem (896px) cap would be unmistakable in the numbers.
test.use({ viewport: { width: 1600, height: 900 } });

async function projectWithWorkstream(
  request: APIRequestContext,
): Promise<string> {
  const dir = await tempProjectDir('hs-ui-width-');
  const created = await request.post('/api/projects', { data: { path: dir } });
  expect(created.status()).toBe(201);
  const projectId = (await created.json()).id as string;

  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'ui-width', config: { branch: 'main' } },
  });
  expect(res.status()).toBe(201);

  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const ws = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find((w: { id: string }) => !w.id.startsWith('creating-'));
    if (ws) return basename(dir);
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error('timed out waiting for the workstream');
}

async function openProject(page: Page, dirName: string) {
  await page.goto('/');
  await selectProject(page, dirName);
}

async function widthOf(locator: Locator): Promise<number> {
  const box = await locator.boundingBox();
  if (!box) throw new Error('element has no box');
  return box.width;
}

test('a workstream card fills the content area', async ({ page, request }) => {
  const dirName = await projectWithWorkstream(request);
  await openProject(page, dirName);

  const card = page.getByRole('region', { name: 'Workstream ui-width' });
  await expect(card).toBeVisible();

  const [cardWidth, mainWidth] = await Promise.all([
    widthOf(card),
    widthOf(page.getByRole('main')),
  ]);

  // The card spans the main region bar its padding, which is the rule; the exact
  // padding is not, so this is a proportion rather than a pixel budget that would
  // break on any spacing tweak. At this viewport it is also well past the 56rem
  // cap that used to be here, which a ratio this tight cannot satisfy.
  expect(cardWidth).toBeGreaterThan(mainWidth * 0.9);
});

test('the new-workstream form spans the view, as the cards do', async ({
  page,
  request,
}) => {
  const dirName = await projectWithWorkstream(request);
  await openProject(page, dirName);

  await page.getByRole('button', { name: /New workstream/i }).click();
  const name = page.getByLabel(/^Name/i);
  await expect(name).toBeVisible({ timeout: 20_000 });

  const form = page.getByRole('form', { name: 'New workstream' });
  const card = page.getByRole('region', { name: 'Workstream ui-width' });
  const [formWidth, cardWidth] = await Promise.all([
    widthOf(form),
    widthOf(card),
  ]);

  // The form sits in the list beside the cards and spans it as they do. A
  // proportion, for the same reason as above: the rule is "as wide as the
  // cards", not a pixel budget that would break on any spacing tweak.
  expect(formWidth).toBeGreaterThan(cardWidth * 0.95);
});

test('the new-session form spans its card', async ({ page, request }) => {
  const dirName = await projectWithWorkstream(request);
  await openProject(page, dirName);

  const card = page.getByRole('region', { name: 'Workstream ui-width' });
  await expect(card).toBeVisible();
  await card.getByRole('button', { name: 'New session' }).click();

  // The form appears once the daemon has answered with this workstream's session
  // options, which is a round trip after the click.
  const form = page.getByRole('form', { name: 'New session' });
  await expect(form).toBeVisible({ timeout: 20_000 });

  const [formWidth, cardWidth] = await Promise.all([
    widthOf(form),
    widthOf(card),
  ]);

  // The form spans its card rather than sitting in a column inside it. A
  // proportion, for the same reason as above: the rule is "as wide as the card",
  // not "within 8px of it".
  expect(formWidth).toBeGreaterThan(cardWidth * 0.95);
});

test("the new-session form's fields span it too", async ({ page, request }) => {
  const dirName = await projectWithWorkstream(request);
  await openProject(page, dirName);

  const card = page.getByRole('region', { name: 'Workstream ui-width' });
  await expect(card).toBeVisible();
  await card.getByRole('button', { name: 'New session' }).click();

  const form = page.getByRole('form', { name: 'New session' });
  await expect(form).toBeVisible({ timeout: 20_000 });

  // A control that is really a control, not a pill row: the fake provider's
  // OpenCode option carries a model enum with no default, which `SchemaField`
  // renders as a <select> with a "Default" entry (the other options' enums have
  // defaults and become pills). It is the field kind a cap would show up on.
  await form.getByRole('radio', { name: 'OpenCode' }).click();
  const select = form.locator('select').first();
  await expect(select).toBeVisible();

  const [formWidth, selectWidth] = await Promise.all([
    widthOf(form),
    widthOf(select),
  ]);

  // The field fills the panel bar the form's own padding and border, which is the
  // rule; the exact inset is not, so it is allowed a small slack rather than
  // pinned to today's spacing.
  expect(selectWidth).toBeGreaterThan(formWidth * 0.9);
});
