import { basename } from 'node:path';
import { expect, tempProjectDir, test } from '../fixtures';
import { awaitRealWorkstream } from '../api/workstream-helpers';
import { loadApp } from './keyboard-helpers';
import {
  materializedWorkstream,
  openWorkstreamCard,
  selectProject,
  unique,
} from './workstream-helpers';
import { addEmptyProject } from '../projects';

/**
 * The workstream card's header: name and branch, its sessions counted by
 * state, then its actions — with no footer below the sessions.
 */

test('the sessions are counted by state beside the name, with no footer', async ({
  page,
  request,
}) => {
  const { card } = await openWorkstreamCard(
    page,
    request,
    'hs-ui-layout',
    'layout',
    2,
  );
  const badges = card.getByTestId('workstream-sessions');
  await expect(badges).toHaveAttribute('aria-label', /^Sessions: 2 [a-z ]+$/);
  // On the header's line, not below the sessions.
  const header = await card.locator('header').boundingBox();
  const box = await badges.boundingBox();
  expect(box && header).toBeTruthy();
  if (box && header) {
    expect(box.y).toBeGreaterThanOrEqual(header.y);
    expect(box.y + box.height).toBeLessThanOrEqual(header.y + header.height);
  }
  await expect(card.getByText(/^\d+ sessions?$/)).toHaveCount(0);
});

test('a branch named like its workstream is not repeated', async ({
  page,
  request,
}) => {
  const dir = await tempProjectDir('hs-ui-layout-branch-');
  const projectId = await addEmptyProject(request, dir);
  const same = unique('same');
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: same, config: { branch: same } },
  });
  await awaitRealWorkstream(request, projectId);
  const different = unique('different');
  await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: different, config: { branch: 'main' } },
  });

  await loadApp(page);
  await selectProject(page, basename(dir));
  const sameCard = page.getByRole('region', { name: `Workstream ${same}` });
  const differentCard = page.getByRole('region', {
    name: `Workstream ${different}`,
  });
  await expect(differentCard.getByText('main', { exact: true })).toBeVisible({
    timeout: 15_000,
  });
  await expect(sameCard.getByText(same, { exact: true })).toHaveCount(1);
});

test('a selected, focused card draws one mark, not a ring around a border', async ({
  page,
  request,
}) => {
  const { card } = await openWorkstreamCard(
    page,
    request,
    'hs-ui-layout-mark',
    'mark',
    0,
  );
  await card.focus();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('ArrowUp');
  await expect(card).toBeFocused();
  await expect(card).toHaveAttribute('aria-current', 'true');
  const outline = await card.evaluate(
    (element) =>
      (
        globalThis as unknown as {
          getComputedStyle: (e: unknown) => { outlineStyle: string };
        }
      ).getComputedStyle(element).outlineStyle,
  );
  expect(outline).toBe('none');
});

// The fake's workstream has no default open, so Open is the menu alone.
test('the Open menu reads "Open", with its caret by the word', async ({
  page,
  request,
}) => {
  const { card, row, workstreamName } = await openWorkstreamCard(
    page,
    request,
    'hs-ui-layout-open',
    'open',
  );
  const trigger = card.getByRole('button', {
    name: `Open workstream ${workstreamName}`,
    exact: true,
  });
  await expect(trigger).toHaveAttribute('aria-haspopup', 'menu');
  const [word, caret, plain] = await Promise.all([
    trigger.getByText('Open', { exact: true }).boundingBox(),
    trigger.getByText('▾').boundingBox(),
    // The same word on a plain button: the width "Open" takes.
    row
      .getByRole('button', { name: 'Open', exact: true })
      .getByText('Open', { exact: true })
      .boundingBox(),
  ]);
  if (!word || !caret || !plain) throw new Error('element has no box');
  // The word, then the one gap (0.35rem) before the caret: split by the gap
  // as "O pen", it ran two gaps wider.
  expect(caret.x - word.x).toBeLessThanOrEqual(plain.width + 7);
});

// The halves share an edge; the hovered or focused one draws its whole border
// (or focus ring) over it.
test('a hovered or focused half of the split Open shows its whole border', async ({
  page,
  request,
}) => {
  const { dirName, workstreamName } = await materializedWorkstream(
    request,
    'hs-ui-layout-split-',
    'split',
  );
  await loadApp(page);
  await selectProject(page, dirName);
  const card = page.getByRole('region', {
    name: `Workstream ${workstreamName}`,
  });
  const name = `Open workstream ${workstreamName}`;
  const primary = card.getByRole('button', { name, exact: true });
  const caret = card.getByRole('button', { name: `${name} with…` });
  const box = await primary.boundingBox();
  if (!box) throw new Error('the split Open has no box');
  const seam = { x: box.x + box.width - 0.5, y: box.y + box.height / 2 };
  // Whether half is what is drawn on top at the shared edge, and is raised.
  const onTop = (half: typeof primary) =>
    half.evaluate((element, { x, y }) => {
      const view = globalThis as unknown as {
        document: { elementFromPoint: (x: number, y: number) => unknown };
        getComputedStyle: (e: unknown) => { zIndex: string };
      };
      const top = view.document.elementFromPoint(x, y);
      return (
        (element as { contains: (other: unknown) => boolean }).contains(top) &&
        view.getComputedStyle(element).zIndex === '1'
      );
    }, seam);

  for (const half of [primary, caret]) {
    await half.hover();
    expect(await onTop(half)).toBe(true);
  }

  await page.mouse.move(0, 0);
  await caret.focus();
  await page.keyboard.press('Shift+Tab');
  await expect(primary).toBeFocused();
  expect(await onTop(primary)).toBe(true);
  await page.keyboard.press('Tab');
  await expect(caret).toBeFocused();
  expect(await onTop(caret)).toBe(true);
});

test('header session counts never pulse, while the session rows do', async ({
  page,
}) => {
  await loadApp(page);
  await selectProject(page, 'homeshell');
  const card = page.getByRole('region', {
    name: 'Workstream feature/self-update',
  });
  const countDots = card.getByTestId('workstream-sessions').getByRole('img');
  await expect(countDots.first()).toBeVisible();
  for (const dot of await countDots.all()) {
    await expect(dot).toHaveCSS('animation-name', 'none');
  }
  // The row dots come after the header in the document.
  const rowDot = card.getByRole('img', { name: 'Running', exact: true }).last();
  await expect(rowDot).not.toHaveCSS('animation-name', 'none');
});
